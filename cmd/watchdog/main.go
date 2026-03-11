package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/skua/biocom/internal/config"
	"github.com/skua/biocom/internal/docker"
	"github.com/skua/biocom/internal/store"
	"github.com/skua/biocom/internal/watchdog"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	logger.Info("BIOCOM WATCHDOG initializing...")

	cfg, err := config.LoadWatchdog(logger)
	if err != nil {
		logger.Error("Configuration error", "error", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Open SQLite store
	dbPath := os.Getenv("BIOCOM_DB")
	if dbPath == "" {
		dbPath = "/app/data/biocom.db"
	}

	st, err := store.Open(dbPath)
	if err != nil {
		logger.Error("Failed to open database", "error", err)
		os.Exit(1)
	}
	defer st.Close()

	// Docker client (required for watchdog)
	dockerClient, err := docker.New(ctx)
	if err != nil {
		logger.Error("Failed to connect to container runtime", "error", err)
		os.Exit(1)
	}
	defer dockerClient.Close()
	logger.Info("Connected to container runtime", "runtime", dockerClient.Runtime())

	// Config file watcher for live reloads
	go func() {
		if err := cfg.WatchFile(ctx); err != nil {
			logger.Error("Config file watcher failed", "error", err)
		}
	}()

	// Run watchdog
	wdCfg := cfg.Watchdog()
	if !wdCfg.Enabled {
		logger.Warn("Watchdog is disabled in config, exiting")
		return
	}
	if len(wdCfg.Containers) == 0 {
		logger.Warn("No containers configured for monitoring, exiting")
		return
	}

	wd := watchdog.New(cfg, dockerClient, st, logger.With("component", "watchdog"))
	go wd.Run(ctx)

	logger.Info("BIOCOM WATCHDOG operational", "containers", wdCfg.Containers)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	logger.Info("Shutdown signal received")
	cancel()
	logger.Info("BIOCOM WATCHDOG shutdown complete")
}
