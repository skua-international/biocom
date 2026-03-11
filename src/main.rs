mod bot;
mod config;
mod docker;
mod watchdog;

use crate::bot::Bot;
use crate::config::{Config, SharedConfig};
use crate::docker::DockerClient;
use crate::watchdog::Watchdog;
use anyhow::Result;
use serenity::prelude::*;
use std::sync::Arc;
use tracing::{error, info, warn};
use tracing_subscriber::{fmt, prelude::*, EnvFilter};

#[tokio::main]
async fn main() -> Result<()> {
    // Setup structured logging (JSON format)
    tracing_subscriber::registry()
        .with(EnvFilter::from_default_env().add_directive("biocom=info".parse()?))
        .with(fmt::layer().json())
        .init();

    info!("BIOCOM initializing...");

    // Load configuration
    let config_path =
        std::env::var("BIOCOM_CONFIG").unwrap_or_else(|_| "biocom.toml".to_string());

    let config = Config::load(&config_path)?;
    let shared_config = SharedConfig::new(config.clone(), config_path.clone());

    // Initialize Docker client
    let docker = match DockerClient::new().await {
        Ok(d) => {
            info!(runtime = d.runtime(), "Connected to container runtime");
            Some(d)
        }
        Err(e) => {
            warn!(error = %e, "Failed to connect to container runtime");
            None
        }
    };

    // Create bot
    let docker = docker.map(Arc::new);
    let bot = Bot::new(shared_config.clone(), docker.clone());

    // Build serenity client
    let intents = GatewayIntents::GUILDS | GatewayIntents::GUILD_MESSAGES;

    let mut client = Client::builder(&config.discord_token, intents)
        .event_handler(bot)
        .await?;

    info!("Discord client built");

    // Get HTTP handle for watchdog before starting client
    let http = client.http.clone();

    // Start config file watcher
    let config_watcher = shared_config.clone();
    tokio::spawn(async move {
        config_watcher.watch().await;
    });

    // Start watchdog if configured
    let watchdog_config = config.watchdog.clone();
    if watchdog_config.enabled && docker.is_some() && !watchdog_config.containers.is_empty() {
        let docker_arc = docker.clone().unwrap();
        let watchdog = Arc::new(Watchdog::new(
            shared_config.clone(),
            docker_arc,
            http,
        ));
        info!(containers = ?watchdog_config.containers, "Watchdog enabled");
        tokio::spawn(async move {
            watchdog.run().await;
        });
    } else if watchdog_config.enabled && docker.is_none() {
        warn!("Watchdog enabled but no container runtime available");
    }

    info!("BIOCOM operational");

    // Start the client (blocks until shutdown)
    if let Err(e) = client.start().await {
        error!(error = %e, "Client error");
    }

    info!("BIOCOM shutdown complete");
    Ok(())
}
