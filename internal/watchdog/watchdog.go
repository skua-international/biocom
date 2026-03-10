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
	w.logger.Info("Watchdog started",
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

	// Warmup: establish baseline state without alerting
	w.warmup(ctx)

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
		w.mu.Unlock()

		// Set initial state without alerting
		switch {
		case info == nil:
			// Container does not exist — mark as already down
			state.wasDown = true
			w.logger.Info("Watchdog warmup: container not found", "container", name)

		case info.State == "running", info.State == "created":
			// Healthy
			state.wasDown = false

		case info.State == "restarting":
			// Already restarting — mark and track
			state.wasDown = true
			state.restartSeen = time.Now()
			w.logger.Info("Watchdog warmup: container restarting", "container", name)

		default:
			// exited, paused, dead, etc. — mark as down
			state.wasDown = true
			w.logger.Info("Watchdog warmup: container down", "container", name, "state", info.State)
		}
	}

	w.logger.Info("Watchdog warmup complete", "containers", len(cfg.Containers))
}

// check inspects all watched containers and sends alerts as needed.
func (w *Watchdog) check(ctx context.Context) {
	cfg := w.cfgSource.Watchdog()

	w.logger.Info("Watchdog tick", "containers", len(cfg.Containers), "enabled", cfg.Enabled)

	if !cfg.Enabled || len(cfg.Containers) == 0 {
		return
	}

	// Ensure states map has entries for current container list
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
			w.logger.Error("Watchdog inspect failed", "container", name, "error", err)
			continue
		}

		w.mu.Lock()
		state := w.states[name]
		w.mu.Unlock()

		now := time.Now()

		switch {
		case info == nil:
			// Container does not exist
			if !state.wasDown {
				w.alert(fmt.Sprintf("🔴 **CONTAINER NOT FOUND:** `%s`\nContainer does not exist or has been removed.", name))
				state.wasDown = true
				state.lastAlerted = now
				state.restartSeen = time.Time{}
			}
			state.runningSince = time.Time{}

		case info.State == "running", info.State == "created":
			if !state.wasDown {
				// Healthy and not recovering — reset tracking
				state.runningSince = time.Time{}
				state.restartSeen = time.Time{}
				break
			}

			// Was down — wait for stable running before declaring recovery
			if state.runningSince.IsZero() {
				state.runningSince = now
			}
			if now.Sub(state.runningSince) >= stableThreshold {
				w.alert(fmt.Sprintf("🟢 **CONTAINER RECOVERED:** `%s`\nStatus: `%s`", name, info.Status))
				state.wasDown = false
				state.runningSince = time.Time{}
				state.restartSeen = time.Time{}
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
				w.alert(fmt.Sprintf(
					"🟡 **CONTAINER STUCK RESTARTING:** `%s`\nRestarting for %s. Status: `%s`",
					name,
					now.Sub(state.restartSeen).Round(time.Second),
					info.Status,
				))
				state.wasDown = true
				state.lastAlerted = now
			}

		default:
			// exited, paused, dead, created, etc.
			if !state.wasDown {
				w.alert(fmt.Sprintf("🔴 **CONTAINER DOWN:** `%s`\nState: `%s` — Status: `%s`", name, info.State, info.Status))
				state.wasDown = true
				state.lastAlerted = now
				state.restartSeen = time.Time{}
			}
			state.runningSince = time.Time{}
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

	_, err := w.session.ChannelMessageSend(cfg.AlertChannelID, msg)
	if err != nil {
		w.logger.Error("Watchdog failed to send alert",
			"channel", cfg.AlertChannelID,
			"error", err,
		)
	} else {
		w.logger.Info("Watchdog alert sent",
			"channel", cfg.AlertChannelID,
			"message", strings.SplitN(message, "\n", 2)[0],
		)
	}
}
