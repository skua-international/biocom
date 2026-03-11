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
	"github.com/skua/biocom/internal/store"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	logger.Info("BIOCOM initializing...")

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
	} else {
		logger.Info("Connected to container runtime", "runtime", dockerClient.Runtime())
	}

	// Create and start bot
	b, err := bot.New(cfg, dockerClient, logger)
	if err != nil {
		logger.Error("Failed to create bot", "error", err)
		os.Exit(1)
	}

	if err := b.Start(ctx); err != nil {
		logger.Error("Failed to start bot", "error", err)
		os.Exit(1)
	}

	logger.Info("BIOCOM operational")

	// Config file watcher for live reloads
	go func() {
		if err := cfg.WatchFile(ctx); err != nil {
			logger.Error("Config file watcher failed", "error", err)
		}
	}()

	// Open SQLite store and start alert poller
	dbPath := os.Getenv("BIOCOM_DB")
	if dbPath == "" {
		dbPath = "/app/data/biocom.db"
	}

	st, err := store.Open(dbPath)
	if err != nil {
		logger.Error("Failed to open alert database", "error", err)
		os.Exit(1)
	}
	defer st.Close()

	go pollAlerts(ctx, st, b.Session(), cfg, logger.With("component", "poller"))

	// Wait for shutdown signal
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	logger.Info("Shutdown signal received")

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
