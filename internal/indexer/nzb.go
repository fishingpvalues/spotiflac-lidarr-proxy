package indexer

import (
	"encoding/xml"
	"fmt"
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

// GenerateNZB builds a minimal, spec-valid synthetic NZB embedding
// spotifyURL, name, and category as <head> metadata. date is a unix
// timestamp (caller-supplied so this stays free of wall-clock calls).
func GenerateNZB(spotifyURL, name, category string, date int64) ([]byte, error) {
	doc := nzbDocument{
		Xmlns: nzbXMLNamespace,
		Head: nzbHead{
			Meta: []nzbMeta{
				{Type: "spotify_url", Value: spotifyURL},
				{Type: "name", Value: name},
				{Type: "category", Value: category},
			},
		},
		Files: []nzbFile{
			{
				Poster:  "spotiflac-lidarr-proxy",
				Date:    date,
				Subject: name,
				Groups:  nzbGroups{Group: []string{"alt.binaries.sounds.flac"}},
				Segments: nzbSegments{Segment: []nzbSegment{
					{Bytes: 1, Number: 1, Value: "placeholder@spotiflac-lidarr-proxy"},
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

// ExtractSpotifyURLFromNZB parses NZB content (as produced by GenerateNZB)
// and returns the embedded spotify_url meta value.
func ExtractSpotifyURLFromNZB(data []byte) (string, error) {
	var doc nzbDocument
	if err := xml.Unmarshal(data, &doc); err != nil {
		return "", fmt.Errorf("parse nzb: %w", err)
	}
	for _, m := range doc.Head.Meta {
		if m.Type == "spotify_url" {
			return m.Value, nil
		}
	}
	return "", fmt.Errorf("no spotify_url meta in nzb")
}
