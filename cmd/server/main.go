package main

import (
	"fmt"
	"os"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/config"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("spotiflac-lidarr-proxy starting on port %d\n", cfg.Port)
}
