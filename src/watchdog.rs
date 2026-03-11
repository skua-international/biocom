use crate::config::SharedConfig;
use crate::docker::DockerClient;
use chrono::Utc;
use serenity::all::{ChannelId, Http};
use std::collections::HashMap;
use std::sync::Arc;
use std::time::{Duration, Instant};
use tokio::sync::Mutex;
use tracing::{error, info};

/// Stable threshold: container must stay running this long before recovery is announced.
const STABLE_THRESHOLD: Duration = Duration::from_secs(60);

#[derive(Default)]
struct ContainerState {
    was_down: bool,
    last_alerted: Option<Instant>,
    restart_seen: Option<Instant>,
    running_since: Option<Instant>,
}

pub struct Watchdog {
    config: SharedConfig,
    docker: Arc<DockerClient>,
    http: Arc<Http>,
    states: Mutex<HashMap<String, ContainerState>>,
}

impl Watchdog {
    pub fn new(config: SharedConfig, docker: Arc<DockerClient>, http: Arc<Http>) -> Self {
        Self {
            config,
            docker,
            http,
            states: Mutex::new(HashMap::new()),
        }
    }

    pub async fn run(self: Arc<Self>) {
        let config = self.config.get().await;

        info!(
            interval = ?config.watchdog_interval(),
            containers = ?config.watchdog.containers,
            channel = config.watchdog.alert_channel_id,
            "Watchdog started"
        );

        // Warmup: establish baseline and alert for currently down containers
        self.warmup().await;

        let mut interval = tokio::time::interval(config.watchdog_interval());

        loop {
            interval.tick().await;
            self.check().await;
        }
    }

    async fn warmup(&self) {
        let config = self.config.get().await;

        if !config.watchdog.enabled || config.watchdog.containers.is_empty() {
            return;
        }

        // Initialize state map
        {
            let mut states = self.states.lock().await;
            for name in &config.watchdog.containers {
                states.entry(name.clone()).or_default();
            }
        }

        for name in &config.watchdog.containers {
            let info = match self.docker.inspect_by_name(name).await {
                Ok(i) => i,
                Err(e) => {
                    error!(container = name, error = %e, "Watchdog warmup inspect failed");
                    continue;
                }
            };

            let mut states = self.states.lock().await;
            let state = states.entry(name.clone()).or_default();

            info!(
                container = name,
                info_nil = info.is_none(),
                was_down_before = state.was_down,
                "Watchdog warmup checking container"
            );

            match info {
                None => {
                    // Container doesn't exist
                    if !state.was_down {
                        state.was_down = true;
                        state.last_alerted = Some(Instant::now());
                        drop(states); // Release lock before sending
                        info!(container = name, "Watchdog warmup: alerting for missing container");
                        self.alert(&format!(
                            "🔴 **CONTAINER NOT FOUND:** `{}`\nContainer does not exist or has been removed.",
                            name
                        ))
                        .await;
                    }
                }
                Some(ref c) if c.state == "running" || c.state == "created" => {
                    state.was_down = false;
                }
                Some(ref c) if c.state == "restarting" => {
                    if !state.was_down {
                        state.was_down = true;
                        state.restart_seen = Some(Instant::now());
                        state.last_alerted = Some(Instant::now());
                        let status = c.status.clone();
                        drop(states);
                        info!(container = name, "Watchdog warmup: container restarting");
                        self.alert(&format!(
                            "🟡 **CONTAINER STUCK RESTARTING:** `{}`\nStatus: `{}`",
                            name, status
                        ))
                        .await;
                    }
                }
                Some(ref c) => {
                    // exited, paused, dead, etc.
                    if !state.was_down {
                        state.was_down = true;
                        state.last_alerted = Some(Instant::now());
                        let container_state = c.state.clone();
                        let status = c.status.clone();
                        drop(states);
                        info!(container = name, state = container_state, "Watchdog warmup: container down");
                        self.alert(&format!(
                            "🔴 **CONTAINER DOWN:** `{}`\nState: `{}` — Status: `{}`",
                            name, container_state, status
                        ))
                        .await;
                    }
                }
            }
        }

        info!(containers = config.watchdog.containers.len(), "Watchdog warmup complete");
    }

    async fn check(&self) {
        let config = self.config.get().await;

        if !config.watchdog.enabled || config.watchdog.containers.is_empty() {
            return;
        }

        // Ensure states map has entries for current container list
        {
            let mut states = self.states.lock().await;
            for name in &config.watchdog.containers {
                states.entry(name.clone()).or_default();
            }
        }

        let restart_timeout = config.restart_timeout();

        for name in &config.watchdog.containers {
            let info = match self.docker.inspect_by_name(name).await {
                Ok(i) => i,
                Err(e) => {
                    error!(container = name, error = %e, "Watchdog inspect failed");
                    continue;
                }
            };

            let now = Instant::now();

            let mut states = self.states.lock().await;
            let state = states.entry(name.clone()).or_default();

            match info {
                None => {
                    // Container doesn't exist
                    state.running_since = None;
                    if !state.was_down {
                        state.was_down = true;
                        state.last_alerted = Some(now);
                        state.restart_seen = None;
                        drop(states);
                        self.alert(&format!(
                            "🔴 **CONTAINER NOT FOUND:** `{}`\nContainer does not exist or has been removed.",
                            name
                        ))
                        .await;
                    }
                }
                Some(ref c) if c.state == "running" || c.state == "created" => {
                    if !state.was_down {
                        // Healthy and not recovering
                        state.running_since = None;
                        state.restart_seen = None;
                    } else {
                        // Was down — wait for stable running before declaring recovery
                        if state.running_since.is_none() {
                            state.running_since = Some(now);
                        }
                        if let Some(since) = state.running_since {
                            if now.duration_since(since) >= STABLE_THRESHOLD {
                                state.was_down = false;
                                state.running_since = None;
                                state.restart_seen = None;
                                let status = c.status.clone();
                                drop(states);
                                self.alert(&format!(
                                    "🟢 **CONTAINER RECOVERED:** `{}`\nStatus: `{}`",
                                    name, status
                                ))
                                .await;
                            }
                        }
                    }
                }
                Some(ref c) if c.state == "restarting" => {
                    if state.restart_seen.is_none() {
                        state.restart_seen = Some(now);
                    }
                    state.running_since = None;

                    // Alert once when restart exceeds the timeout
                    let stuck = state
                        .restart_seen
                        .map(|s| now.duration_since(s) >= restart_timeout)
                        .unwrap_or(false);

                    if stuck && !state.was_down {
                        state.was_down = true;
                        state.last_alerted = Some(now);
                        let duration = state
                            .restart_seen
                            .map(|s| now.duration_since(s))
                            .unwrap_or_default();
                        let status = c.status.clone();
                        drop(states);
                        self.alert(&format!(
                            "🟡 **CONTAINER STUCK RESTARTING:** `{}`\nRestarting for {:?}. Status: `{}`",
                            name, duration, status
                        ))
                        .await;
                    }
                }
                Some(ref c) => {
                    // exited, paused, dead, etc.
                    state.running_since = None;
                    if !state.was_down {
                        state.was_down = true;
                        state.last_alerted = Some(now);
                        state.restart_seen = None;
                        let container_state = c.state.clone();
                        let status = c.status.clone();
                        drop(states);
                        self.alert(&format!(
                            "🔴 **CONTAINER DOWN:** `{}`\nState: `{}` — Status: `{}`",
                            name, container_state, status
                        ))
                        .await;
                    }
                }
            }
        }
    }

    async fn alert(&self, message: &str) {
        let config = self.config.get().await;

        if config.watchdog.alert_channel_id.is_empty() {
            info!(message = message, "Watchdog alert skipped: no alert channel configured");
            return;
        }

        let channel_id: u64 = match config.watchdog.alert_channel_id.parse() {
            Ok(id) => id,
            Err(_) => {
                error!("Invalid alert channel ID");
                return;
            }
        };

        let role_ping = if !config.watchdog.alert_role_id.is_empty() {
            format!("<@&{}> ", config.watchdog.alert_role_id)
        } else {
            String::new()
        };

        let timestamp = Utc::now().format("%Y-%m-%dT%H:%M:%SZ");
        let mut msg = format!(
            "{}⚠️ **BIOCOM WATCHDOG**\n{}\n— {}",
            role_ping, message, timestamp
        );

        if msg.len() > 1900 {
            msg.truncate(1900);
            msg.push_str("\n…truncated");
        }

        info!(
            channel = config.watchdog.alert_channel_id,
            role = config.watchdog.alert_role_id,
            message = message.lines().next().unwrap_or(""),
            "Watchdog sending alert"
        );

        let channel = ChannelId::new(channel_id);
        if let Err(e) = channel.say(&self.http, &msg).await {
            error!(
                channel = config.watchdog.alert_channel_id,
                error = %e,
                "Watchdog failed to send alert"
            );
        }
    }
}
