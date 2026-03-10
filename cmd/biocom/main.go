package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/skua/biocom/internal/bot"
	"github.com/skua/biocom/internal/config"
	"github.com/skua/biocom/internal/docker"
	"github.com/skua/biocom/internal/watchdog"
)

func main() {
	// Setup structured logging
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	logger.Info("BIOCOM initializing...")

	// Load configuration
	cfg, err := config.Load(logger)
	if err != nil {
		logger.Error("Configuration error", "error", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Initialize Docker client
	dockerClient, err := docker.New(ctx)
	if err != nil {
		logger.Warn("Failed to connect to container runtime", "error", err)
		// Continue without Docker - some commands will be unavailable
	} else {
		logger.Info("Connected to container runtime", "runtime", dockerClient.Runtime())
	}

	// Create bot
	b, err := bot.New(cfg, dockerClient, logger)
	if err != nil {
		logger.Error("Failed to create bot", "error", err)
		os.Exit(1)
	}

	// Start bot
	if err := b.Start(ctx); err != nil {
		logger.Error("Failed to start bot", "error", err)
		os.Exit(1)
	}

	logger.Info("BIOCOM operational")

	// Start config file watcher for live reloads
	go func() {
		if err := cfg.WatchFile(ctx); err != nil {
			logger.Error("Config file watcher failed", "error", err)
		}
	}()

	// Start watchdog if configured
	wdCfg := cfg.Watchdog()
	if wdCfg.Enabled && dockerClient != nil && len(wdCfg.Containers) > 0 {
		wd := watchdog.New(cfg, dockerClient, b.Session(), logger.With("component", "watchdog"))
		go wd.Run(ctx)
		logger.Info("Watchdog enabled", "containers", wdCfg.Containers)
	} else if wdCfg.Enabled && dockerClient == nil {
		logger.Warn("Watchdog enabled but no container runtime available")
	}

	// Wait for shutdown signal
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	logger.Info("Shutdown signal received")

	// Graceful shutdown
	if err := b.Stop(); err != nil {
		logger.Error("Error during shutdown", "error", err)
	}

	if dockerClient != nil {
		if err := dockerClient.Close(); err != nil {
			logger.Error("Error closing Docker client", "error", err)
		}
	}

	logger.Info("BIOCOM shutdown complete")
}