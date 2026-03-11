package watchdog

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/skua/biocom/internal/config"
	"github.com/skua/biocom/internal/docker"
)

// stableThreshold is how long a container must stay running before we
// consider it recovered. Prevents flapping containers from generating
// repeated down/recovery alerts.
const stableThreshold = 60 * time.Second

// containerState tracks per-container alert state.
type containerState struct {
	lastAlerted  time.Time
	wasDown      bool
	restartSeen  time.Time // first time we saw "restarting"
	runningSince time.Time // when we first saw "running" after a down event
}

// Watchdog monitors containers and alerts via Discord.
type Watchdog struct {
	cfgSource    *config.Config
	dockerClient *docker.Client
	session      *discordgo.Session
	logger       *slog.Logger

	mu     sync.Mutex
	states map[string]*containerState
}

// New creates a new Watchdog instance.
func New(cfgSource *config.Config, dockerClient *docker.Client, session *discordgo.Session, logger *slog.Logger) *Watchdog {
	return &Watchdog{
		cfgSource:    cfgSource,
		dockerClient: dockerClient,
		session:      session,
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
		"channel", cfg.AlertChannelID,
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

// warmup establishes baseline container state without sending alerts.
// This prevents spurious alerts for containers that are already down when
// the watchdog starts.
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
		wasDownBefore := state.wasDown

		w.logger.Info("Watchdog warmup checking container",
			"container", name,
			"info_nil", info == nil,
			"wasDown_before", wasDownBefore,
		)

		// Set initial state and alert for downed containers (only if not already marked)
		switch {
		case info == nil:
			// Container does not exist — alert once, mark as down
			if !state.wasDown {
				state.wasDown = true
				state.lastAlerted = time.Now()
				w.mu.Unlock()
				w.logger.Info("Watchdog warmup: alerting for missing container", "container", name)
				w.alert(fmt.Sprintf("🔴 **CONTAINER NOT FOUND:** `%s`\nContainer does not exist or has been removed.", name))
			} else {
				w.mu.Unlock()
				w.logger.Debug("Watchdog warmup: skipping alert, already marked down", "container", name)
			}

		case info.State == "running", info.State == "created":
			// Healthy
			state.wasDown = false
			w.mu.Unlock()

		case info.State == "restarting":
			// Already restarting — alert once, mark and track
			if !state.wasDown {
				state.wasDown = true
				state.restartSeen = time.Now()
				state.lastAlerted = time.Now()
				w.mu.Unlock()
				w.alert(fmt.Sprintf("🟡 **CONTAINER STUCK RESTARTING:** `%s`\nStatus: `%s`", name, info.Status))
				w.logger.Info("Watchdog warmup: container restarting", "container", name)
			} else {
				w.mu.Unlock()
			}

		default:
			// exited, paused, dead, etc. — alert once, mark as down
			if !state.wasDown {
				state.wasDown = true
				state.lastAlerted = time.Now()
				w.mu.Unlock()
				w.alert(fmt.Sprintf("🔴 **CONTAINER DOWN:** `%s`\nState: `%s` — Status: `%s`", name, info.State, info.Status))
				w.logger.Info("Watchdog warmup: container down", "container", name, "state", info.State)
			} else {
				w.mu.Unlock()
			}
		}
	}

	w.logger.Info("Watchdog warmup complete", "containers", len(cfg.Containers))
}

// check inspects all watched containers and sends alerts as needed.
func (w *Watchdog) check(ctx context.Context) {
	cfg := w.cfgSource.Watchdog()

	w.logger.Debug("Watchdog check() called", "containers", len(cfg.Containers), "enabled", cfg.Enabled, "states", w.states)

	if !cfg.Enabled || len(cfg.Containers) == 0 {
		return
	}

	// Sync states map with current container list
	w.mu.Lock()
	// Add new containers
	for _, name := range cfg.Containers {
		if _, ok := w.states[name]; !ok {
			w.states[name] = &containerState{}
		}
	}
	// Remove containers no longer in config
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
		wasDownBefore := state.wasDown

		w.logger.Debug("Watchdog check() examining container",
			"container", name,
			"info_nil", info == nil,
			"wasDown", wasDownBefore,
		)

		switch {
		case info == nil:
			// Container does not exist
			state.runningSince = time.Time{}
			if !state.wasDown {
				state.wasDown = true
				state.lastAlerted = now
				state.restartSeen = time.Time{}
				w.mu.Unlock()
				w.logger.Info("Watchdog check(): alerting for missing container", "container", name)
				w.alert(fmt.Sprintf("🔴 **CONTAINER NOT FOUND:** `%s`\nContainer does not exist or has been removed.", name))
			} else {
				w.mu.Unlock()
				w.logger.Debug("Watchdog check(): skipping alert, wasDown=true", "container", name)
			}

		case info.State == "running", info.State == "created":
			if !state.wasDown {
				// Healthy and not recovering — reset tracking
				state.runningSince = time.Time{}
				state.restartSeen = time.Time{}
				w.mu.Unlock()
				break
			}

			// Was down — wait for stable running before declaring recovery
			if state.runningSince.IsZero() {
				state.runningSince = now
			}
			if now.Sub(state.runningSince) >= stableThreshold {
				state.wasDown = false
				state.runningSince = time.Time{}
				state.restartSeen = time.Time{}
				w.mu.Unlock()
				w.alert(fmt.Sprintf("🟢 **CONTAINER RECOVERED:** `%s`\nStatus: `%s`", name, info.Status))
			} else {
				w.mu.Unlock()
			}

		case info.State == "restarting":
			// Track how long it's been restarting
			if state.restartSeen.IsZero() {
				state.restartSeen = now
			}
			state.runningSince = time.Time{}

			// Alert once when restart exceeds the timeout
			stuck := now.Sub(state.restartSeen) >= cfg.RestartTTL
			if stuck && !state.wasDown {
				state.wasDown = true
				state.lastAlerted = now
				w.mu.Unlock()
				w.alert(fmt.Sprintf(
					"🟡 **CONTAINER STUCK RESTARTING:** `%s`\nRestarting for %s. Status: `%s`",
					name,
					now.Sub(state.restartSeen).Round(time.Second),
					info.Status,
				))
			} else {
				w.mu.Unlock()
			}

		default:
			// exited, paused, dead, created, etc.
			state.runningSince = time.Time{}
			if !state.wasDown {
				state.wasDown = true
				state.lastAlerted = now
				state.restartSeen = time.Time{}
				w.mu.Unlock()
				w.alert(fmt.Sprintf("🔴 **CONTAINER DOWN:** `%s`\nState: `%s` — Status: `%s`", name, info.State, info.Status))
			} else {
				w.mu.Unlock()
			}
		}
	}
}

// alert sends a message to the configured alert channel.
func (w *Watchdog) alert(message string) {
	cfg := w.cfgSource.Watchdog()

	if cfg.AlertChannelID == "" {
		w.logger.Warn("Watchdog alert skipped: no alert channel configured", "message", message)
		return
	}

	// Mention role if configured
	rolePing := ""
	if cfg.AlertRoleID != "" {
		rolePing = fmt.Sprintf("<@&%s> ", cfg.AlertRoleID)
	}

	msg := fmt.Sprintf("%s⚠️ **BIOCOM WATCHDOG**\n%s\n— %s", rolePing, message, time.Now().UTC().Format(time.RFC3339))

	// Discord 2000 char limit
	if len(msg) > 1900 {
		msg = msg[:1900] + "\n…truncated"
	}

	w.logger.Info("Watchdog sending alert",
		"channel", cfg.AlertChannelID,
		"role", cfg.AlertRoleID,
		"message", strings.SplitN(message, "\n", 2)[0],
	)

	_, err := w.session.ChannelMessageSend(cfg.AlertChannelID, msg)
	if err != nil {
		w.logger.Error("Watchdog failed to send alert",
			"channel", cfg.AlertChannelID,
			"error", err,
		)
	}
}
