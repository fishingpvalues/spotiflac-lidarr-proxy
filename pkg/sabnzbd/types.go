package sabnzbd

type JobStatus string

const (
	StatusQueued      JobStatus = "Queued"
	StatusDownloading JobStatus = "Downloading"
	StatusCompleted   JobStatus = "Completed"
	StatusFailed      JobStatus = "Failed"
	StatusPaused      JobStatus = "Paused"
)

type VersionResponse struct {
	Version string `json:"version"`
}

type AuthResponse struct {
	Auth bool `json:"auth"`
}

type AddURLResponse struct {
	Status bool     `json:"status"`
	NzoIDs []string `json:"nzo_ids"`
}

type CategoriesResponse struct {
	Categories []string `json:"categories"`
}

// QueueResponse matches SABnzbd queue output. Lidarr expects:
// - size/sizeleft/mb/mbleft as float64 (MB values)
// - timeleft as "HH:MM:SS" format (parsed as TimeSpan in C#)
// - diskspace fields as float64 (GB values)
// - status values: Downloading, Queued, Paused, Propagating, Fetching
type QueueResponse struct {
	Queue Queue `json:"queue"`
}

type Queue struct {
	Status         string  `json:"status"`
	Speedlimit     string  `json:"speedlimit"`
	SpeedlimitAbs  string  `json:"speedlimit_abs"`
	Paused         bool    `json:"paused"`
	Noofslots      int     `json:"noofslots"`
	NoofslotsTotal int     `json:"noofslots_total"`
	Limit          int     `json:"limit"`
	Start          int     `json:"start"`
	Timeleft       string  `json:"timeleft"`
	Speed          string  `json:"speed"`
	Kbpersec       string  `json:"kbpersec"`
	Size           string  `json:"size"`
	Sizeleft       string  `json:"sizeleft"`
	Mb             float64 `json:"mb"`
	Mbleft         float64 `json:"mbleft"`
	Slots          []Slot  `json:"slots"`
	Diskspace1     float64 `json:"diskspace1"`
	Diskspace2     float64 `json:"diskspace2"`
	Diskspacetotal1 float64 `json:"diskspacetotal1"`
	Diskspacetotal2 float64 `json:"diskspacetotal2"`
	Version             string  `json:"version"`
	DefaultRootFolder   string  `json:"defaultrootfolder,omitempty"`
	Finish              int     `json:"finish"`
	PausedAll           bool    `json:"paused_all"`
}

type Slot struct {
	Status       string   `json:"status"`
	Index        int      `json:"index"`
	NzoID        string   `json:"nzo_id"`
	Filename     string   `json:"filename"`
	Size         string   `json:"size"`
	Sizeleft     string   `json:"sizeleft"`
	Mb           float64  `json:"mb"`
	Mbleft       float64  `json:"mbleft"`
	Percentage   string   `json:"percentage"`
	Timeleft     string   `json:"timeleft"`
	Priority     string   `json:"priority"`
	Cat          string   `json:"cat"`
	Labels       []string `json:"labels"`
	TimeAdded    int64    `json:"time_added"`
	Script       string   `json:"script"`
	Unpackopts   string   `json:"unpackopts"`
	Password     string   `json:"password"`
	AvgAge       string   `json:"avg_age"`
	DirectUnpack string   `json:"direct_unpack"`
	Mbmissing    float64  `json:"mbmissing"`
}

// HistoryResponse matches SABnzbd history output. Lidarr expects:
// - size as int64 (bytes, used directly without MB conversion)
// - storage as the filesystem path for remote path mapping
// - fail_message set on failed items
// - status: Completed, Failed, Verifying, Moving, etc.
type HistoryResponse struct {
	History History `json:"history"`
}

type History struct {
	Noofslots int           `json:"noofslots"`
	TotalSize string        `json:"total_size"`
	MonthSize string        `json:"month_size"`
	WeekSize  string        `json:"week_size"`
	Slots     []HistorySlot `json:"slots"`
	Version   string        `json:"version"`
}

type HistorySlot struct {
	Status       string `json:"status"`
	NzoID        string `json:"nzo_id"`
	Name         string `json:"name"`
	Size         int64  `json:"size"`
	Cat          string `json:"cat"`
	Completed    int64  `json:"completed"`
	DownloadTime int    `json:"download_time"`
	Script       string `json:"script"`
	Storage      string `json:"storage"`
	Path         string `json:"path"`
	FailMessage  string `json:"fail_message,omitempty"`
	URL          string `json:"url,omitempty"`
}

// FullStatusResponse is used by Lidarr v2.0+ to resolve relative complete_dir.
type FullStatusResponse struct {
	CompleteDir string `json:"complete_dir"`
}

// StatusResponse is a generic status response for queue/history operations.
type StatusResponse struct {
	Status bool     `json:"status"`
	NzoIDs []string `json:"nzo_ids,omitempty"`
	Error  string   `json:"error,omitempty"`
}

type ConfigResponse struct {
	Config Config `json:"config"`
}

type Config struct {
	Categories []Category `json:"categories"`
	Scripts    []Script   `json:"scripts"`
	Speedlimit string     `json:"speedlimit"`
	Misc       Misc       `json:"misc"`
}

type Category struct {
	Name  string `json:"name"`
	Order int    `json:"order"`
	Dir   string `json:"dir"`
}

type Script struct {
	Name    string `json:"name"`
	Default bool   `json:"default"`
}

type Misc struct {
	Version                 string   `json:"version"`
	CompletedDir            string   `json:"complete_dir"`
	CompleteDirEnabled      bool     `json:"complete_dir_enabled"`
	HistoryRetention        string   `json:"history_retention,omitempty"`
	HistoryRetentionOption  string   `json:"history_retention_option,omitempty"`
	HistoryRetentionNumber  int      `json:"history_retention_number,omitempty"`
	PreCheck                bool     `json:"pre_check"`
	TvCategories            []string `json:"tv_categories,omitempty"`
	MovieCategories         []string `json:"movie_categories,omitempty"`
	DateCategories          []string `json:"date_categories,omitempty"`
	EnableTvSorting         bool     `json:"enable_tv_sorting,omitempty"`
	EnableMovieSorting      bool     `json:"enable_movie_sorting,omitempty"`
	EnableDateSorting       bool     `json:"enable_date_sorting,omitempty"`
}

// ServerStatsResponse for mode=server_stats
type ServerStatsResponse struct {
	Total      float64 `json:"total"`
	Month      float64 `json:"month"`
	Week       float64 `json:"week"`
	Daily      float64 `json:"daily"`
	Articles   int     `json:"articles"`
	Speed      string  `json:"speed"`
	Version    string  `json:"version"`
	Day        float64 `json:"day"`
	WeekAccel  float64 `json:"week_accel"`
	MonthAccel float64 `json:"month_accel"`
}

// WarningsResponse for mode=warnings
type WarningsResponse struct {
	Warnings []Warning `json:"warnings"`
}

type Warning struct {
	Time    int64  `json:"time"`
	Type    string `json:"type"`
	Text    string `json:"text"`
	ID      string `json:"id,omitempty"`
	Details string `json:"details,omitempty"`
}

// SimpleStatusResponse for mode=status
type SimpleStatusResponse struct {
	Paused bool `json:"paused"`
}

// RetryResponse for mode=retry
type RetryResponse struct {
	Status bool   `json:"status"`
	Id     string `json:"id,omitempty"`
	NzoID  string `json:"nzo_id,omitempty"`
	Error  string `json:"error,omitempty"`
}
