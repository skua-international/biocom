package config

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/fsnotify/fsnotify"
)

// WatchdogConfig holds watchdog settings from the TOML config.
type WatchdogConfig struct {
	Enabled        bool          `toml:"enabled"`
	IntervalSec    int           `toml:"interval_seconds"`
	AlertChannelID string        `toml:"alert_channel_id"`
	AlertRoleID    string        `toml:"alert_role_id"`
	Containers     []string      `toml:"containers"`
	RestartTimeout int           `toml:"restart_timeout_seconds"`
	Interval       time.Duration `toml:"-"`
	RestartTTL     time.Duration `toml:"-"`
}

// RolesConfig holds per-command-group role ID arrays.
type RolesConfig struct {
	Admin []string `toml:"admin"` // containers, logs
	Zeus  []string `toml:"zeus"`  // intercept, upload_mission, upload_preset
}

// FileConfig represents the full TOML config file.
type FileConfig struct {
	Watchdog WatchdogConfig `toml:"watchdog"`
	Roles    RolesConfig    `toml:"roles"`
}

// Config holds all application configuration.
type Config struct {
	DiscordToken string
	GuildID      string
	UploadBase   string
	MissionsDir  string
	PresetsDir   string

	mu         sync.RWMutex
	watchdog   WatchdogConfig
	roles      RolesConfig
	configPath string
	logger     *slog.Logger
	onChange   []func(*WatchdogConfig)
}

// Watchdog returns a copy of the current watchdog config (thread-safe).
func (c *Config) Watchdog() WatchdogConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.watchdog
}

// WatchdogPtr returns a pointer to a copy of the current watchdog config.
func (c *Config) WatchdogPtr() *WatchdogConfig {
	w := c.Watchdog()
	return &w
}

// Roles returns a copy of the current roles config (thread-safe).
func (c *Config) Roles() RolesConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.roles
}

// OnWatchdogChange registers a callback fired after a successful config reload.
func (c *Config) OnWatchdogChange(fn func(*WatchdogConfig)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onChange = append(c.onChange, fn)
}

// Load reads configuration from environment variables and TOML config file.
func Load(logger *slog.Logger) (*Config, error) {
	token := os.Getenv("DISCORD_TOKEN")
	if token == "" {
		return nil, fmt.Errorf("DISCORD_TOKEN environment variable is required")
	}

	guildID := os.Getenv("SERVER_ID")
	if guildID == "" {
		return nil, fmt.Errorf("SERVER_ID environment variable is required")
	}

	uploadBase := os.Getenv("UPLOAD_BASE")
	if uploadBase == "" {
		uploadBase = "/app/uploads"
	}

	configPath := os.Getenv("BIOCOM_CONFIG")
	if configPath == "" {
		configPath = "biocom.toml"
	}

	cfg := &Config{
		DiscordToken: token,
		GuildID:      guildID,
		UploadBase:   uploadBase,
		MissionsDir:  filepath.Join(uploadBase, "missions"),
		PresetsDir:   filepath.Join(uploadBase, "presets"),
		configPath:   configPath,
		logger:       logger,
	}

	// Ensure upload directories exist
	if err := os.MkdirAll(cfg.MissionsDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create missions directory: %w", err)
	}
	if err := os.MkdirAll(cfg.PresetsDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create presets directory: %w", err)
	}

	// Initial TOML load
	if err := cfg.reload(); err != nil {
		logger.Warn("No config file loaded", "path", configPath, "error", err)
	}

	return cfg, nil
}

// reload reads the TOML file and updates the watchdog config.
func (c *Config) reload() error {
	var fc FileConfig
	if _, err := toml.DecodeFile(c.configPath, &fc); err != nil {
		return err
	}

	w := &fc.Watchdog

	if w.IntervalSec <= 0 {
		w.IntervalSec = 60
	}
	w.Interval = time.Duration(w.IntervalSec) * time.Second

	if w.RestartTimeout <= 0 {
		w.RestartTimeout = 300
	}
	w.RestartTTL = time.Duration(w.RestartTimeout) * time.Second

	c.mu.Lock()
	c.watchdog = fc.Watchdog
	c.roles = fc.Roles
	callbacks := make([]func(*WatchdogConfig), len(c.onChange))
	copy(callbacks, c.onChange)
	c.mu.Unlock()

	// Fire callbacks outside the lock
	for _, fn := range callbacks {
		fn(&fc.Watchdog)
	}

	return nil
}

// WatchFile watches the TOML config file for changes and reloads automatically.
// Blocks until context is cancelled.
func (c *Config) WatchFile(ctx context.Context) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("failed to create file watcher: %w", err)
	}
	defer watcher.Close()

	// Watch the directory (handles editors that write-and-rename)
	dir := filepath.Dir(c.configPath)
	if err := watcher.Add(dir); err != nil {
		return fmt.Errorf("failed to watch directory %s: %w", dir, err)
	}

	base := filepath.Base(c.configPath)
	c.logger.Info("Watching config file for changes", "path", c.configPath)

	// Debounce: editors can fire multiple events in quick succession
	var debounce *time.Timer

	for {
		select {
		case <-ctx.Done():
			return nil

		case event, ok := <-watcher.Events:
			if !ok {
				return nil
			}
			if filepath.Base(event.Name) != base {
				continue
			}
			if event.Op&(fsnotify.Write|fsnotify.Create) == 0 {
				continue
			}

			// Reset debounce timer
			if debounce != nil {
				debounce.Stop()
			}
			debounce = time.AfterFunc(500*time.Millisecond, func() {
				if err := c.reload(); err != nil {
					c.logger.Error("Config reload failed", "error", err)
				} else {
					c.logger.Info("Config reloaded successfully", "path", c.configPath)
				}
			})

		case err, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
			c.logger.Error("File watcher error", "error", err)
		}
	}
}
