package watchdog

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/skua/biocom/internal/config"
	"github.com/skua/biocom/internal/docker"
	"github.com/skua/biocom/internal/store"
)

// stableThreshold is how long a container must stay running before we
// consider it recovered. Prevents flapping containers from generating
// repeated down/recovery alerts.
const stableThreshold = 60 * time.Second

// Down-state reasons. Empty string means the container is healthy.
const (
	reasonNotFound   = "not_found"
	reasonRestarting = "restarting"
	reasonDown       = "down"
)

// containerState tracks per-container alert state.
type containerState struct {
	lastAlerted  time.Time
	downReason   string    // "", "not_found", "restarting", "down"
	restartSeen  time.Time // first time we saw "restarting"
	runningSince time.Time // when we first saw "running" after a down event
}

func (s *containerState) isDown() bool {
	return s.downReason != ""
}

// Watchdog monitors containers and queues alerts via SQLite.
type Watchdog struct {
	cfgSource    *config.Config
	dockerClient *docker.Client
	store        *store.Store
	logger       *slog.Logger

	mu     sync.Mutex
	states map[string]*containerState
}

// New creates a new Watchdog instance.
func New(cfgSource *config.Config, dockerClient *docker.Client, st *store.Store, logger *slog.Logger) *Watchdog {
	return &Watchdog{
		cfgSource:    cfgSource,
		dockerClient: dockerClient,
		store:        st,
		logger:       logger,
		states:       make(map[string]*containerState),
	}
}

// Run starts the watchdog loop. Blocks until context is cancelled.
func (w *Watchdog) Run(ctx context.Context) {
	cfg := w.cfgSource.Watchdog()
	w.logger.Info("Watchdog Run() entered",
		"interval", cfg.Interval,
		"containers", cfg.Containers,
	)

	ticker := time.NewTicker(cfg.Interval)
	defer ticker.Stop()

	// Restart ticker when config changes interval
	w.cfgSource.OnWatchdogChange(func(newCfg *config.WatchdogConfig) {
		ticker.Reset(newCfg.Interval)
		w.logger.Info("Watchdog config reloaded",
			"interval", newCfg.Interval,
			"containers", newCfg.Containers,
		)
	})

	// Warmup: establish baseline state and alert for downed containers
	w.logger.Info("Watchdog calling warmup")
	w.warmup(ctx)
	w.logger.Info("Watchdog warmup returned")

	for {
		select {
		case <-ctx.Done():
			w.logger.Info("Watchdog stopped")
			return
		case <-ticker.C:
			w.check(ctx)
		}
	}
}

// warmup establishes baseline container state. Alerts for containers that
// are already down when the watchdog starts.
func (w *Watchdog) warmup(ctx context.Context) {
	cfg := w.cfgSource.Watchdog()

	if !cfg.Enabled || len(cfg.Containers) == 0 {
		return
	}

	w.mu.Lock()
	for _, name := range cfg.Containers {
		if _, ok := w.states[name]; !ok {
			w.states[name] = &containerState{}
		}
	}
	w.mu.Unlock()

	for _, name := range cfg.Containers {
		info, err := w.dockerClient.InspectByName(ctx, name)
		if err != nil {
			w.logger.Error("Watchdog warmup inspect failed", "container", name, "error", err)
			continue
		}

		w.mu.Lock()
		state := w.states[name]

		w.logger.Info("Watchdog warmup checking container",
			"container", name,
			"info_nil", info == nil,
			"downReason", state.downReason,
		)

		switch {
		case info == nil:
			if state.downReason != reasonNotFound {
				state.downReason = reasonNotFound
				state.lastAlerted = time.Now()
				w.mu.Unlock()
				w.logger.Info("Watchdog warmup: alerting for missing container", "container", name)
				w.queueAlert(ctx, "red", name, "CONTAINER NOT FOUND",
					"Container does not exist or has been removed.")
			} else {
				w.mu.Unlock()
			}

		case info.State == "running", info.State == "created":
			state.downReason = ""
			w.mu.Unlock()

		case info.State == "restarting":
			if state.downReason != reasonRestarting {
				state.downReason = reasonRestarting
				state.restartSeen = time.Now()
				state.lastAlerted = time.Now()
				w.mu.Unlock()
				w.queueAlert(ctx, "yellow", name, "CONTAINER STUCK RESTARTING",
					fmt.Sprintf("Status: `%s`", info.Status))
				w.logger.Info("Watchdog warmup: container restarting", "container", name)
			} else {
				w.mu.Unlock()
			}

		default:
			if state.downReason != reasonDown {
				state.downReason = reasonDown
				state.lastAlerted = time.Now()
				w.mu.Unlock()
				w.queueAlert(ctx, "red", name, "CONTAINER DOWN",
					fmt.Sprintf("State: `%s` — Status: `%s`", info.State, info.Status))
				w.logger.Info("Watchdog warmup: container down", "container", name, "state", info.State)
			} else {
				w.mu.Unlock()
			}
		}
	}

	w.logger.Info("Watchdog warmup complete", "containers", len(cfg.Containers))
}

// check inspects all watched containers and queues alerts as needed.
func (w *Watchdog) check(ctx context.Context) {
	cfg := w.cfgSource.Watchdog()

	w.logger.Debug("Watchdog check() called", "containers", len(cfg.Containers), "enabled", cfg.Enabled)

	if !cfg.Enabled || len(cfg.Containers) == 0 {
		return
	}

	// Sync states map with current container list
	w.mu.Lock()
	for _, name := range cfg.Containers {
		if _, ok := w.states[name]; !ok {
			w.states[name] = &containerState{}
		}
	}
	current := make(map[string]struct{}, len(cfg.Containers))
	for _, name := range cfg.Containers {
		current[name] = struct{}{}
	}
	for name := range w.states {
		if _, ok := current[name]; !ok {
			delete(w.states, name)
		}
	}
	w.mu.Unlock()

	for _, name := range cfg.Containers {
		info, err := w.dockerClient.InspectByName(ctx, name)
		if err != nil {
			w.logger.Error("Watchdog inspect failed", "container", name, "error", err)
			continue
		}

		now := time.Now()

		w.mu.Lock()
		state := w.states[name]

		w.logger.Debug("Watchdog check() examining container",
			"container", name,
			"info_nil", info == nil,
			"downReason", state.downReason,
		)

		switch {
		case info == nil:
			// Container does not exist
			state.runningSince = time.Time{}
			if state.downReason != reasonNotFound {
				state.downReason = reasonNotFound
				state.lastAlerted = now
				state.restartSeen = time.Time{}
				w.mu.Unlock()
				w.queueAlert(ctx, "red", name, "CONTAINER NOT FOUND",
					"Container does not exist or has been removed.")
			} else {
				w.mu.Unlock()
			}

		case info.State == "running", info.State == "created":
			if !state.isDown() {
				if state.runningSince.IsZero() {
					state.runningSince = now
				}
				// Only clear restart tracking after sustained stable running.
				// This prevents crashlooping containers (brief running -> restarting)
				// from resetting the restart timer on every brief "running" blip.
				if now.Sub(state.runningSince) >= stableThreshold {
					state.restartSeen = time.Time{}
					state.runningSince = time.Time{}
				}
				w.mu.Unlock()
				break
			}

			// Was down -- wait for stable running before declaring recovery
			if state.runningSince.IsZero() {
				state.runningSince = now
			}
			if now.Sub(state.runningSince) >= stableThreshold {
				state.downReason = ""
				state.runningSince = time.Time{}
				state.restartSeen = time.Time{}
				w.mu.Unlock()
				w.queueAlert(ctx, "green", name, "CONTAINER RECOVERED",
					fmt.Sprintf("Status: `%s`", info.Status))
			} else {
				w.mu.Unlock()
			}

		case info.State == "restarting":
			if state.restartSeen.IsZero() {
				state.restartSeen = now
			}
			state.runningSince = time.Time{}

			stuck := now.Sub(state.restartSeen) >= cfg.RestartTTL
			if stuck && state.downReason != reasonRestarting {
				state.downReason = reasonRestarting
				state.lastAlerted = now
				w.mu.Unlock()
				w.queueAlert(ctx, "yellow", name, "CONTAINER STUCK RESTARTING",
					fmt.Sprintf("Restarting for %s. Status: `%s`",
						now.Sub(state.restartSeen).Round(time.Second), info.Status))
			} else {
				w.mu.Unlock()
			}

		default:
			// exited, paused, dead, etc.
			state.runningSince = time.Time{}
			if state.downReason != reasonDown {
				state.downReason = reasonDown
				state.lastAlerted = now
				state.restartSeen = time.Time{}
				w.mu.Unlock()
				w.queueAlert(ctx, "red", name, "CONTAINER DOWN",
					fmt.Sprintf("State: `%s` — Status: `%s`", info.State, info.Status))
			} else {
				w.mu.Unlock()
			}
		}
	}
}

// queueAlert inserts an alert into the SQLite store for the bot to pick up.
func (w *Watchdog) queueAlert(ctx context.Context, level, container, title, body string) {
	cfg := w.cfgSource.Watchdog()

	a := &store.Alert{
		Level:     level,
		Container: container,
		Title:     title,
		Body:      body,
		RolePing:  cfg.AlertRoleID != "",
		CreatedAt: time.Now().UTC(),
	}

	id, err := w.store.InsertAlert(ctx, a)
	if err != nil {
		w.logger.Error("Watchdog failed to queue alert", "error", err, "container", container)
		return
	}

	w.logger.Info("Watchdog alert queued",
		"id", id,
		"level", level,
		"container", container,
		"title", title,
	)
}
