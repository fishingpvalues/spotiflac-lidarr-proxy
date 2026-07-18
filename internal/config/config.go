package config

import (
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
