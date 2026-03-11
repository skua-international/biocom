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

// WatchFile polls the TOML config file for changes and reloads automatically.
// Uses content-hash polling since inotify and mtime don't work reliably
// across Docker bind mounts.
// Blocks until context is cancelled.
func (c *Config) WatchFile(ctx context.Context) error {
	const pollInterval = 2 * time.Second

	c.logger.Info("Polling config file for changes", "path", c.configPath, "poll_interval", pollInterval)

	// Seed with current file content hash
	lastHash := c.hashFile()

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			currentHash := c.hashFile()
			if currentHash == lastHash {
				continue
			}
			lastHash = currentHash

			if err := c.reload(); err != nil {
				c.logger.Error("Config reload failed", "error", err)
			} else {
				c.logger.Info("Config reloaded successfully", "path", c.configPath)
			}
		}
	}
}

// hashFile returns a simple hash of the config file contents for change detection.
func (c *Config) hashFile() string {
	data, err := os.ReadFile(c.configPath)
	if err != nil {
		return ""
	}
	// Simple FNV-style hash — no crypto needed, just change detection
	var h uint64 = 14695981039346656037
	for _, b := range data {
		h ^= uint64(b)
		h *= 1099511628211
	}
	return fmt.Sprintf("%x", h)
}
