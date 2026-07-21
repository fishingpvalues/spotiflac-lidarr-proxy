package config

import (
	"strings"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/viper"
)

type Config struct {
	Port             int           `mapstructure:"port"`
	APIKey           string        `mapstructure:"api_key"`
	OutputDir        string        `mapstructure:"output_dir"`
	SpotiflacCLIPath string        `mapstructure:"spotiflac_cli_path"`
	DefaultService   string        `mapstructure:"default_service"`
	DefaultQuality   string        `mapstructure:"default_quality"`
	MaxConcurrent    int           `mapstructure:"max_concurrent"`
	JobTimeout       time.Duration `mapstructure:"job_timeout"`
	DBPath           string        `mapstructure:"db_path"`
	LogLevel         string        `mapstructure:"log_level"`
	TidalAPIURL      string        `mapstructure:"tidal_api_url"`
	QobuzAPIURL      string        `mapstructure:"qobuz_api_url"`
}

func Load() (*Config, error) {
	v := viper.New()
	v.SetEnvPrefix("SPF")
	v.AutomaticEnv()

	setDefaults(v)

	for _, key := range []string{
		"api_key", "port", "output_dir", "spotiflac_cli_path",
		"default_service", "default_quality", "max_concurrent",
		"job_timeout", "db_path", "log_level",
		"tidal_api_url", "qobuz_api_url",
	} {
		v.BindEnv(key)
	}

	cfg := &Config{}
	if err := v.Unmarshal(cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}

	if cfg.APIKey == "" {
		return nil, fmt.Errorf("SPF_API_KEY is required")
	}

	if err := os.MkdirAll(cfg.OutputDir, 0755); err != nil {
		return nil, fmt.Errorf("create output dir %s: %w", cfg.OutputDir, err)
	}

	dbDir := filepath.Dir(cfg.DBPath)
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		return nil, fmt.Errorf("create db dir %s: %w", dbDir, err)
	}

	return cfg, nil
}

func setDefaults(v *viper.Viper) {
	v.SetDefault("port", 8484)
	v.SetDefault("output_dir", "/downloads")
	v.SetDefault("spotiflac_cli_path", "/usr/local/bin/spotiflac-cli")
	v.SetDefault("default_service", "tidal")
	v.SetDefault("default_quality", "lossless")
	v.SetDefault("max_concurrent", 3)
	v.SetDefault("job_timeout", "30m")
	v.SetDefault("db_path", "/data/queue.db")
	v.SetDefault("log_level", "info")
}

// Service constants matching SpotiFLAC CLI
const (
	ServiceTidal  = "tidal"
	ServiceQobuz  = "qobuz"
	ServiceAmazon = "amazon"
	ServiceDeezer = "deezer"
)

// SpotiflacQuality maps proxy quality names → SpotiFLAC CLI --quality values.
// SpotiFLAC CLI expects uppercase: LOSSLESS, HIRES_LOSSLESS.
func SpotiflacQuality(proxyQuality string) string {
	switch proxyQuality {
	case "lossless", "flac-16", "cd", "16":
		return "LOSSLESS"
	case "hires", "hires-lossless", "hires_lossless", "flac-24", "24":
		return "HIRES_LOSSLESS"
	case "both":
		return "HIRES_LOSSLESS"
	default:
		return "LOSSLESS"
	}
}

// ParseCategory extracts service and quality from a SABnzbd category name.
// Categories follow the pattern: music-[service][-quality]
// Examples: music-tidal, music-flac-16, music-qobuz-flac-24
func ParseCategory(cat string) (service, quality string) {
	cat = strings.ToLower(cat)

	// Detect service
	for _, svc := range []string{"tidal", "qobuz", "amazon", "deezer"} {
		if strings.Contains(cat, svc) {
			service = svc
			break
		}
	}

	// Detect quality
	if strings.Contains(cat, "flac-24") || strings.Contains(cat, "hires") || strings.Contains(cat, "24") {
		quality = "hires"
	} else if strings.Contains(cat, "flac-16") || strings.Contains(cat, "lossless") || strings.Contains(cat, "16") {
		quality = "lossless"
	}

	return
}
