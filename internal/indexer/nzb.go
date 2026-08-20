package indexer

import (
	"encoding/xml"
	"fmt"
	"strconv"
)

// Lidarr fetches a Newznab release's enclosure/download URL and validates
// the response is a well-formed NZB (root element "nzb") *before* ever
// contacting the download client - a plain link to the Spotify page (an
// HTML document) fails that check outright ("Expected 'nzb' found
// 'html'"). Since there's no real NZB backing a SpotiFLAC release, this
// generates a minimal, spec-valid NZB whose <head> carries the actual
// Spotify URL and job metadata as <meta> tags. The SABnzbd addfile handler
// (internal/api/sabnzbd/addurl.go) parses it back out with
// ExtractSpotifyURLFromNZB to recover the real download target.

const nzbXMLNamespace = "http://www.newzbin.com/DTD/2003/nzb"

// nzbDoctype is required by several NZB consumers for the document to be
// recognized; placed between the XML declaration and the root element.
const nzbDoctype = `<!DOCTYPE nzb PUBLIC "-//newzBin//DTD NZB 1.1//EN" "http://www.newzbin.com/DTD/nzb/nzb-1.1.dtd">` + "\n"

type nzbDocument struct {
	XMLName xml.Name  `xml:"nzb"`
	Xmlns   string    `xml:"xmlns,attr"`
	Head    nzbHead   `xml:"head"`
	Files   []nzbFile `xml:"file"`
}

type nzbHead struct {
	Meta []nzbMeta `xml:"meta"`
}

type nzbMeta struct {
	Type  string `xml:"type,attr"`
	Value string `xml:",chardata"`
}

type nzbFile struct {
	Poster   string      `xml:"poster,attr"`
	Date     int64       `xml:"date,attr"`
	Subject  string      `xml:"subject,attr"`
	Groups   nzbGroups   `xml:"groups"`
	Segments nzbSegments `xml:"segments"`
}

type nzbGroups struct {
	Group []string `xml:"group"`
}

type nzbSegments struct {
	Segment []nzbSegment `xml:"segment"`
}

type nzbSegment struct {
	Bytes  int64  `xml:"bytes,attr"`
	Number int    `xml:"number,attr"`
	Value  string `xml:",chardata"`
}

// Release carries everything the synthetic NZB has to hand forward to the
// SABnzbd side. Lidarr's mode=addfile request sends no `nzbname` and no size
// (verified against a real grab: the POST query is only
// mode=addfile&cat=&priority=&apikey=&output=json), so anything the download
// client needs about the release has to travel inside the NZB itself.
//
// Without Name the job's Filename stayed empty, the queue slot marshaled
// with `"filename": ""`, and Lidarr - which keys its tracked download off
// that string - never showed the download in its own queue at all. It
// downloaded and imported nothing, over and over.
type Release struct {
	SpotifyURL string
	Name       string
	Category   string
	// Size is the estimated payload in bytes (see EstimateSizeBytes). Real
	// size is only known once the files land, but Lidarr renders "0 B" and
	// computes no progress at all from a zero, so the estimate stands in
	// until the CLI reports the truth.
	Size int64
	// TrackCount is the album's track count, which is what lets the
	// download client verify it got a whole album rather than one file.
	TrackCount int
	Date       int64
}

// GenerateNZBRelease builds a minimal, spec-valid synthetic NZB embedding the
// release's Spotify URL, name, category, estimated size and track count as
// <head> metadata. Date is a unix timestamp (caller-supplied so this stays
// free of wall-clock calls).
func GenerateNZBRelease(r Release) ([]byte, error) {
	meta := []nzbMeta{
		{Type: "spotify_url", Value: r.SpotifyURL},
		{Type: "name", Value: r.Name},
		{Type: "category", Value: r.Category},
	}
	if r.Size > 0 {
		meta = append(meta, nzbMeta{Type: "size", Value: strconv.FormatInt(r.Size, 10)})
	}
	if r.TrackCount > 0 {
		meta = append(meta, nzbMeta{Type: "track_count", Value: strconv.Itoa(r.TrackCount)})
	}

	segmentBytes := r.Size
	if segmentBytes <= 0 {
		segmentBytes = 1
	}

	doc := nzbDocument{
		Xmlns: nzbXMLNamespace,
		Head:  nzbHead{Meta: meta},
		Files: []nzbFile{
			{
				Poster:  "spotiflac-lidarr-proxy",
				Date:    r.Date,
				Subject: r.Name,
				Groups:  nzbGroups{Group: []string{"alt.binaries.sounds.flac"}},
				Segments: nzbSegments{Segment: []nzbSegment{
					{Bytes: segmentBytes, Number: 1, Value: "placeholder@spotiflac-lidarr-proxy"},
				}},
			},
		},
	}

	body, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal nzb: %w", err)
	}
	return []byte(xml.Header + nzbDoctype + string(body)), nil
}

// GenerateNZB is the pre-Release shorthand, kept for callers that have no
// size or track count to pass.
func GenerateNZB(spotifyURL, name, category string, date int64) ([]byte, error) {
	return GenerateNZBRelease(Release{
		SpotifyURL: spotifyURL,
		Name:       name,
		Category:   category,
		Date:       date,
	})
}

// ParseNZBMeta reads back everything GenerateNZBRelease embedded. SpotifyURL
// is the only required field: an NZB without it is not one of ours.
func ParseNZBMeta(data []byte) (Release, error) {
	var doc nzbDocument
	if err := xml.Unmarshal(data, &doc); err != nil {
		return Release{}, fmt.Errorf("parse nzb: %w", err)
	}
	var r Release
	for _, m := range doc.Head.Meta {
		switch m.Type {
		case "spotify_url":
			r.SpotifyURL = m.Value
		case "name":
			r.Name = m.Value
		case "category":
			r.Category = m.Value
		case "size":
			r.Size, _ = strconv.ParseInt(m.Value, 10, 64)
		case "track_count":
			r.TrackCount, _ = strconv.Atoi(m.Value)
		}
	}
	if r.SpotifyURL == "" {
		return Release{}, fmt.Errorf("no spotify_url meta in nzb")
	}
	return r, nil
}

// ExtractSpotifyURLFromNZB parses NZB content (as produced by
// GenerateNZBRelease) and returns the embedded spotify_url meta value.
func ExtractSpotifyURLFromNZB(data []byte) (string, error) {
	r, err := ParseNZBMeta(data)
	if err != nil {
		return "", err
	}
	return r.SpotifyURL, nil
}
