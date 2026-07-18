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

type QueueResponse struct {
	Queue Queue `json:"queue"`
}

type Queue struct {
	Status           string  `json:"status"`
	Speedlimit       string  `json:"speedlimit"`
	SpeedlimitAbs    string  `json:"speedlimit_abs"`
	Paused           bool    `json:"paused"`
	Noofslots        int     `json:"noofslots"`
	NoofslotsTotal   int     `json:"noofslots_total"`
	Limit            int     `json:"limit"`
	Start            int     `json:"start"`
	Timeleft         string  `json:"timeleft"`
	Speed            string  `json:"speed"`
	Kbpersec         string  `json:"kbpersec"`
	Size             string  `json:"size"`
	Sizeleft         string  `json:"sizeleft"`
	Mb               string  `json:"mb"`
	Mbleft           string  `json:"mbleft"`
	Slots            []Slot  `json:"slots"`
	Diskspace1       string  `json:"diskspace1"`
	Diskspace2       string  `json:"diskspace2"`
	Diskspacetotal1  string  `json:"diskspacetotal1"`
	Diskspacetotal2  string  `json:"diskspacetotal2"`
	Version          string  `json:"version"`
	Finish           int     `json:"finish"`
	PausedAll        bool    `json:"paused_all"`
}

type Slot struct {
	Status       string   `json:"status"`
	Index        int      `json:"index"`
	NzoID        string   `json:"nzo_id"`
	Filename     string   `json:"filename"`
	Size         string   `json:"size"`
	Sizeleft     string   `json:"sizeleft"`
	Mb           string   `json:"mb"`
	Mbleft       string   `json:"mbleft"`
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
	Mbmissing    string   `json:"mbmissing"`
}

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
	Size         string `json:"size"`
	Cat          string `json:"cat"`
	Completed    int64  `json:"completed"`
	DownloadTime int    `json:"download_time"`
	Script       string `json:"script"`
	Storage      string `json:"storage"`
	Path         string `json:"path"`
	FailMessage  string `json:"fail_message,omitempty"`
	URL          string `json:"url,omitempty"`
}

type StatusResponse struct {
	Status bool     `json:"status"`
	NzoIDs []string `json:"nzo_ids,omitempty"`
	Error  string   `json:"error,omitempty"`
}

type ConfigResponse struct {
	Config struct {
		Categories []Category `json:"categories"`
		Scripts    []Script   `json:"scripts"`
		Speedlimit string     `json:"speedlimit"`
		Misc       Misc       `json:"misc"`
	} `json:"config"`
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
	Version            string `json:"version"`
	CompletedDir       string `json:"completed_dir"`
	DownloadDir        string `json:"download_dir"`
	CompleteDirEnabled bool   `json:"complete_dir_enabled"`
}
