package indexer

import (
	"encoding/xml"
	"fmt"
	"time"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/spotiflac"
)

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
	GUID        string    `xml:"guid"`
	Link        string    `xml:"link"`
	PubDate     string    `xml:"pubDate"`
	Category    string    `xml:"category"`
	Description string    `xml:"description"`
	Enclosure   Enclosure `xml:"enclosure"`
	Attrs       []Attr    `xml:"newznab:attr"`
}

type Enclosure struct {
	URL    string `xml:"url,attr"`
	Length string `xml:"length,attr"`
	Type   string `xml:"type,attr"`
}

type Attr struct {
	Name  string `xml:"name,attr"`
	Value string `xml:"value,attr"`
}

func NewznabXML(results []spotiflac.MetadataResult, serverURL string) ([]byte, error) {
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

	for _, r := range results {
		item := Item{
			Title:       r.Artist + " - " + r.Album,
			GUID:        r.SpotifyURL,
			Link:        r.SpotifyURL,
			PubDate:     time.Now().Format(time.RFC1123Z),
			Category:    "Music > " + r.Genre,
			Description: fmt.Sprintf("%s - %s (%d tracks)", r.Artist, r.Album, r.TrackCount),
			Enclosure: Enclosure{
				URL:    r.SpotifyURL,
				Length: "0",
				Type:   "application/x-nzb",
			},
			Attrs: []Attr{
				{Name: "artist", Value: r.Artist},
				{Name: "album", Value: r.Album},
				{Name: "genre", Value: r.Genre},
				{Name: "year", Value: fmt.Sprintf("%d", r.Year)},
			},
		}
		if r.CoverURL != "" {
			item.Attrs = append(item.Attrs, Attr{Name: "coverurl", Value: r.CoverURL})
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

func CapsXML(serverURL string) []byte {
	xml := `<?xml version="1.0" encoding="UTF-8"?>
<caps>
  <server title="Spotiflac-Lidarr Proxy" version="0.1.0" url="` + serverURL + `" />
  <searching>
    <search available="yes" supported="yes" />
    <music-search available="yes" supported="yes" />
  </searching>
  <categories>
    <category id="3000" name="Audio">
      <subcat id="3010" name="Lossless" />
      <subcat id="3040" name="Flac" />
    </category>
  </categories>
</caps>`
	return []byte(xml)
}
