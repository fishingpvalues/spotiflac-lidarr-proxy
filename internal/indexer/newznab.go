package indexer

import (
	"encoding/xml"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/spotiflac"
)

const (
	avgBytesPerTrackLossless = 35 * 1024 * 1024 // ~35MB/track, 16-bit FLAC estimate
	avgBytesPerTrackHires    = 90 * 1024 * 1024 // ~90MB/track, 24-bit hi-res FLAC estimate
)

// EstimateSizeBytes gives a rough, clearly-approximate release size so
// Lidarr's size-based checks don't see a hard 0. Not exact — SpotiFLAC's
// search output doesn't expose real payload size ahead of download.
func EstimateSizeBytes(trackCount int, quality string) int64 {
	if trackCount <= 0 {
		return 0
	}
	perTrack := int64(avgBytesPerTrackLossless)
	if quality == "hires" {
		perTrack = avgBytesPerTrackHires
	}
	return int64(trackCount) * perTrack
}

type RSS struct {
	XMLName xml.Name `xml:"rss"`
	Version string   `xml:"version,attr"`
	Atom    string   `xml:"xmlns:atom,attr"`
	Newznab string   `xml:"xmlns:newznab,attr"`
	Channel Channel  `xml:"channel"`
}

type Channel struct {
	Title       string   `xml:"title"`
	Description string   `xml:"description"`
	Link        string   `xml:"link"`
	Language    string   `xml:"language"`
	WebMaster   string   `xml:"webMaster"`
	Category    string   `xml:"category"`
	Image       Image    `xml:"image"`
	Response    Response `xml:"newznab:response"`
	Items       []Item   `xml:"item"`
}

type Image struct {
	URL         string `xml:"url"`
	Title       string `xml:"title"`
	Link        string `xml:"link"`
	Description string `xml:"description"`
}

type Response struct {
	Offset int `xml:"offset,attr"`
	Total  int `xml:"total,attr"`
}

type Item struct {
	Title       string    `xml:"title"`
	GUID        GUID      `xml:"guid"`
	Link        string    `xml:"link"`
	PubDate     string    `xml:"pubDate"`
	Category    string    `xml:"category"`
	Description string    `xml:"description"`
	Comments    string    `xml:"comments,omitempty"`
	Enclosure   Enclosure `xml:"enclosure"`
	Attrs       []Attr    `xml:"newznab:attr"`
}

type GUID struct {
	Value       string `xml:",chardata"`
	IsPermaLink bool   `xml:"isPermaLink,attr"`
}

type Enclosure struct {
	URL    string `xml:"url,attr"`
	Length int64  `xml:"length,attr"`
	Type   string `xml:"type,attr"`
}

type Attr struct {
	Name  string `xml:"name,attr"`
	Value string `xml:"value,attr"`
}

// newznabCategories returns the numeric category ids to advertise per item,
// parent first, matching what CapsXML declares.
//
// Consumers read an item's categories ONLY from `newznab:attr name="category"`
// (Lidarr's NewznabRssParser.GetCategory, and the same in Prowlarr, autobrr and
// cross-seed); the human-readable <category> element is decorative. Without
// these attrs every release we hand out parses with an empty category set, so
// any consumer that filters or routes on category silently drops it.
//
// This is NOT what makes Lidarr's indexer Test button complain. That message
// ("no results in the configured categories") is emitted whenever the result set
// is empty, and Lidarr tests with a browse query - no artist, no album - which
// this indexer answers with zero items by design. See README.
func newznabCategories(quality string) []string {
	if quality == "hires" {
		return []string{"3000", "3040"}
	}
	return []string{"3000", "3010"}
}

// qualityTag returns a release-title suffix Lidarr's QualityParser will
// recognize (verified against its actual regexes: CodecRegex matches
// "flac" case-insensitively, SampleSizeRegex matches "24-bit" /
// "24bit"). Without any recognizable token in the title, Lidarr parses
// the release as Quality.Unknown and most quality profiles reject it
// outright ("Unknown is not wanted in profile") - confirmed against a
// real production Lidarr this session: a real grab attempt succeeded
// past NZB validation but was rejected for exactly this reason.
func qualityTag(quality string) string {
	if quality == "hires" {
		return " [FLAC 24-bit]"
	}
	return " [FLAC]"
}

// yearAttr renders the release year, or the empty string when it is unknown.
// A literal "0" is worse than nothing: Lidarr parses the attr into its
// release year and then scores the album against it, so an unknown year that
// claims to be year zero fails the year component of its album-match check
// outright ("Album match is not close enough: 77.6 % vs 80 % [year, country,
// tracks]" - seen against production on every SpotiFLAC grab).
func yearAttr(year int) string {
	if year <= 0 {
		return ""
	}
	return strconv.Itoa(year)
}

// categoryLabel renders the decorative <category> element. Without a genre
// the old unconditional "Music > " + genre left a dangling separator
// ("Music > ") on every single item.
func categoryLabel(genre string) string {
	if genre == "" {
		return "Music"
	}
	return "Music > " + genre
}

func NewznabXML(results []spotiflac.MetadataResult, serverURL, apiKey, quality string) ([]byte, error) {
	if results == nil {
		results = []spotiflac.MetadataResult{}
	}

	rss := RSS{
		Version: "2.0",
		Atom:    "http://www.w3.org/2005/Atom",
		Newznab: "http://www.newznab.com/DTD/2010/feeds/attributes/",
		Channel: Channel{
			Title:       "Spotiflac-Lidarr Proxy",
			Description: "Spotify metadata via SpotiFLAC",
			Link:        serverURL,
			Language:    "en-us",
			WebMaster:   "admin@spotiflac-proxy",
			Category:    "music",
			Image: Image{
				URL:         serverURL + "/static/logo.png",
				Title:       "Spotiflac-Lidarr Proxy",
				Link:        serverURL,
				Description: "Spotiflac-Lidarr Proxy",
			},
			Response: Response{
				Offset: 0,
				Total:  len(results),
			},
		},
	}

	titleSuffix := qualityTag(quality)
	categories := newznabCategories(quality)

	for _, r := range results {
		estimatedSize := EstimateSizeBytes(r.TrackCount, quality)
		title := r.Artist + " - " + r.Album + titleSuffix
		attrs := make([]Attr, 0, len(categories)+10)
		for _, cat := range categories {
			attrs = append(attrs, Attr{Name: "category", Value: cat})
		}
		attrs = append(attrs, []Attr{
			{Name: "artist", Value: r.Artist},
			{Name: "album", Value: r.Album},
			{Name: "genre", Value: r.Genre},
			{Name: "year", Value: yearAttr(r.Year)},
			{Name: "title", Value: title},
			{Name: "size", Value: fmt.Sprintf("%d", estimatedSize)},
			{Name: "grabs", Value: "0"},
			{Name: "files", Value: fmt.Sprintf("%d", r.TrackCount)},
			{Name: "poster", Value: r.CoverURL},
		}...)
		if r.ISRC != "" {
			attrs = append(attrs, Attr{Name: "isrc", Value: r.ISRC})
		}

		// Lidarr fetches this URL itself and requires a well-formed NZB
		// (root element "nzb") before it will even contact the download
		// client - the raw Spotify page is HTML and fails that check
		// outright. handleGet (t=get) generates a synthetic NZB carrying
		// r.SpotifyURL as embedded metadata; see nzb.go.
		// name/size/tracks ride along on the download URL because that is
		// the only channel there is. Lidarr fetches this URL verbatim, and
		// its follow-up mode=addfile POST carries no nzbname and no size -
		// so t=get folds these into the NZB it returns, and the SABnzbd
		// side reads them back out (indexer.ParseNZBMeta). Without the
		// name the queue slot's filename is empty and Lidarr never tracks
		// the download at all.
		params := url.Values{}
		params.Set("t", "get")
		params.Set("id", r.SpotifyURL)
		params.Set("name", title)
		if estimatedSize > 0 {
			params.Set("size", strconv.FormatInt(estimatedSize, 10))
		}
		if r.TrackCount > 0 {
			params.Set("tracks", strconv.Itoa(r.TrackCount))
		}
		params.Set("apikey", apiKey)
		downloadURL := serverURL + "/api/newznab?" + params.Encode()

		item := Item{
			Title:       title,
			GUID:        GUID{Value: r.SpotifyURL, IsPermaLink: true},
			Link:        downloadURL,
			PubDate:     time.Now().Format(time.RFC1123Z),
			Category:    categoryLabel(r.Genre),
			Description: fmt.Sprintf("%s - %s (%d tracks)", r.Artist, r.Album, r.TrackCount),
			Comments:    "",
			Enclosure: Enclosure{
				URL:    downloadURL,
				Length: estimatedSize,
				Type:   "application/x-nzb",
			},
			Attrs: attrs,
		}

		rss.Channel.Items = append(rss.Channel.Items, item)
	}

	output, err := xml.MarshalIndent(rss, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal newznab xml: %w", err)
	}

	result := xml.Header + string(output)
	return []byte(result), nil
}

func CapsXML(serverURL, version string) []byte {
	xmlStr := `<?xml version="1.0" encoding="UTF-8"?>
<caps>
  <server title="Spotiflac-Lidarr Proxy" version="` + version + `" url="` + serverURL + `" />
  <searching>
    <search available="yes" supported="yes" />
    <music-search available="yes" supported="yes" supportedParams="q,artist,album" />
    <audio-search available="yes" supported="yes" supportedParams="q,artist,album" />
  </searching>
  <categories>
    <category id="3000" name="Audio">
      <subcat id="3010" name="Lossless"/>
      <subcat id="3040" name="FLAC 24-bit"/>
      <subcat id="3050" name="FLAC 16-bit"/>
      <subcat id="3060" name="Tidal"/>
      <subcat id="3061" name="Qobuz"/>
      <subcat id="3062" name="Amazon"/>
      <subcat id="3063" name="Deezer"/>
    </category>
  </categories>
</caps>`
	return []byte(xmlStr)
}
