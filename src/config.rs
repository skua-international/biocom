use anyhow::{Context, Result};
use serde::Deserialize;
use std::collections::HashSet;
use std::path::Path;
use std::sync::Arc;
use std::time::Duration;
use tokio::sync::RwLock;
use tracing::{info, warn};

#[derive(Debug, Clone, Deserialize)]
pub struct Config {
    pub roles: RolesConfig,
    pub watchdog: WatchdogConfig,

    #[serde(skip)]
    pub discord_token: String,
    #[serde(skip)]
    pub guild_id: u64,
    #[serde(skip)]
    pub upload_base: String,
}

#[derive(Debug, Clone, Deserialize)]
pub struct RolesConfig {
    #[serde(default)]
    pub admin: Vec<String>,
    #[serde(default)]
    pub zeus: Vec<String>,
}

#[derive(Debug, Clone, Deserialize)]
pub struct WatchdogConfig {
    #[serde(default)]
    pub enabled: bool,

    #[serde(default = "default_interval")]
    pub interval_seconds: u64,

    #[serde(default)]
    pub alert_channel_id: String,

    #[serde(default)]
    pub alert_role_id: String,

    #[serde(default = "default_restart_timeout")]
    pub restart_timeout_seconds: u64,

    #[serde(default)]
    pub containers: Vec<String>,
}

fn default_interval() -> u64 {
    30
}

fn default_restart_timeout() -> u64 {
    60
}

impl Config {
    pub fn load<P: AsRef<Path>>(path: P) -> Result<Self> {
        let content = std::fs::read_to_string(path.as_ref())
            .with_context(|| format!("Failed to read config file: {:?}", path.as_ref()))?;

        let mut config: Config =
            toml::from_str(&content).with_context(|| "Failed to parse config file")?;

        // Load from environment
        config.discord_token = std::env::var("DISCORD_TOKEN")
            .with_context(|| "DISCORD_TOKEN environment variable not set")?;

        config.guild_id = std::env::var("SERVER_ID")
            .with_context(|| "SERVER_ID environment variable not set")?
            .parse()
            .with_context(|| "SERVER_ID must be a valid u64")?;

        config.upload_base =
            std::env::var("UPLOAD_BASE").unwrap_or_else(|_| "/app/uploads".to_string());

        Ok(config)
    }

    pub fn admin_roles(&self) -> HashSet<u64> {
        self.roles
            .admin
            .iter()
            .filter_map(|s| s.parse().ok())
            .collect()
    }

    pub fn zeus_roles(&self) -> HashSet<u64> {
        self.roles
            .zeus
            .iter()
            .filter_map(|s| s.parse().ok())
            .collect()
    }

    pub fn watchdog_interval(&self) -> Duration {
        Duration::from_secs(self.watchdog.interval_seconds)
    }

    pub fn restart_timeout(&self) -> Duration {
        Duration::from_secs(self.watchdog.restart_timeout_seconds)
    }
}

/// Shared configuration that can be reloaded at runtime.
#[derive(Clone)]
pub struct SharedConfig {
    inner: Arc<RwLock<Config>>,
    path: String,
}

impl SharedConfig {
    pub fn new(config: Config, path: String) -> Self {
        Self {
            inner: Arc::new(RwLock::new(config)),
            path,
        }
    }

    pub async fn get(&self) -> Config {
        self.inner.read().await.clone()
    }

    pub async fn reload(&self) -> Result<()> {
        let new_config = Config::load(&self.path)?;
        let mut guard = self.inner.write().await;
        *guard = new_config;
        info!("Configuration reloaded");
        Ok(())
    }

    /// Start polling for config changes.
    pub async fn watch(self) {
        let poll_interval = Duration::from_secs(2);
        let mut last_modified = std::fs::metadata(&self.path)
            .and_then(|m| m.modified())
            .ok();

        loop {
            tokio::time::sleep(poll_interval).await;

            let current_modified = std::fs::metadata(&self.path)
                .and_then(|m| m.modified())
                .ok();

            if current_modified != last_modified {
                if let Err(e) = self.reload().await {
                    warn!("Failed to reload config: {}", e);
                } else {
                    last_modified = current_modified;
                }
            }
        }
    }
}
