package queue

import (
	"time"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/pkg/sabnzbd"
)

type Job struct {
	ID           int64              `json:"-"`
	NzoID        string             `json:"nzo_id"`
	SpotifyURL   string             `json:"spotify_url"`
	Status       sabnzbd.JobStatus  `json:"status"`
	Category     string             `json:"category"`
	Priority     string             `json:"priority"`
	Filename     string             `json:"filename"`
	OutputPath   string             `json:"output_path"`
	Size         int64              `json:"size"`
	Sizeleft     int64              `json:"sizeleft"`
	Percentage   float64            `json:"percentage"`
	TimeAdded    time.Time          `json:"time_added"`
	CompletedAt  *time.Time         `json:"completed_at,omitempty"`
	ErrorMessage string             `json:"error_message,omitempty"`
	Service      string             `json:"service"`
	Quality      string             `json:"quality"`
}

type ListParams struct {
	Start    int
	Limit    int
	Search   string
	NzoIDs   []string
	Status   string
	Category string
}
