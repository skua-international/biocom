use crate::config::SharedConfig;
use crate::docker::DockerClient;
use anyhow::Result;
use regex::Regex;
use serenity::all::{
    CommandInteraction, CommandOptionType, Context, CreateAttachment,
    CreateCommand, CreateCommandOption, CreateInteractionResponse,
    CreateInteractionResponseMessage, EditInteractionResponse,
    GuildId, Http, Interaction, Member, Ready, ResolvedValue,
};
use std::sync::Arc;
use serenity::async_trait;
use serenity::prelude::*;
use std::path::Path;
use tokio::fs;
use tracing::{error, info, warn};
use urlencoding::decode;

pub struct Bot {
    config: SharedConfig,
    docker: Option<Arc<DockerClient>>,
}

impl Bot {
    pub fn new(config: SharedConfig, docker: Option<Arc<DockerClient>>) -> Self {
        Self {
            config,
            docker,
        }
    }

    async fn register_commands(&self, http: &Http, guild_id: GuildId) -> Result<()> {
        let commands = vec![
            CreateCommand::new("ping").description("Check if BIOCOM is alive"),
            CreateCommand::new("intercept")
                .description("Intercept communication")
                .add_option(
                    CreateCommandOption::new(
                        CommandOptionType::String,
                        "message",
                        "Post a message to the specified channel or thread",
                    )
                    .required(true),
                )
                .add_option(
                    CreateCommandOption::new(
                        CommandOptionType::Channel,
                        "channel",
                        "Optional channel or thread (defaults to current)",
                    )
                    .required(false),
                ),
            CreateCommand::new("upload_mission")
                .description("Upload a mission (.pbo)")
                .add_option(
                    CreateCommandOption::new(
                        CommandOptionType::Attachment,
                        "file",
                        "Mission file (.pbo)",
                    )
                    .required(true),
                ),
            CreateCommand::new("upload_preset")
                .description("Upload a preset (.html)")
                .add_option(
                    CreateCommandOption::new(
                        CommandOptionType::Attachment,
                        "file",
                        "Preset file (.html)",
                    )
                    .required(true),
                ),
            CreateCommand::new("containers")
                .description("List running Docker containers on the BIOCOM stack"),
            CreateCommand::new("logs")
                .description("Pull container logs")
                .add_option(
                    CreateCommandOption::new(
                        CommandOptionType::String,
                        "container",
                        "Container name",
                    )
                    .required(true),
                )
                .add_option(
                    CreateCommandOption::new(
                        CommandOptionType::String,
                        "filter",
                        "Regex filter (queries all lines, returns matches)",
                    )
                    .required(false),
                ),
        ];

        guild_id.set_commands(http, commands).await?;
        Ok(())
    }

    fn has_any_role(member: &Member, role_ids: &std::collections::HashSet<u64>) -> bool {
        member.roles.iter().any(|r| role_ids.contains(&r.get()))
    }

    fn sanitize_filename(raw: &str) -> String {
        let decoded = decode(raw).unwrap_or_else(|_| raw.into());
        let basename = Path::new(decoded.as_ref())
            .file_name()
            .and_then(|s| s.to_str())
            .unwrap_or(&decoded);

        let re = Regex::new(r"[^A-Za-z0-9 ._-]").unwrap();
        re.replace_all(basename, "").trim().to_string()
    }

    async fn handle_ping(&self, ctx: &Context, command: &CommandInteraction) -> Result<()> {
        command
            .create_response(
                &ctx.http,
                CreateInteractionResponse::Message(
                    CreateInteractionResponseMessage::new()
                        .content("BIOCOM: STATUS OPERATIONAL. GLOBAL LOOP.")
                        .ephemeral(true),
                ),
            )
            .await?;
        Ok(())
    }

    async fn handle_intercept(&self, ctx: &Context, command: &CommandInteraction) -> Result<()> {
        let config = self.config.get().await;
        let guild_id: u64 = command.guild_id.map(|g| g.get()).unwrap_or(0);

        if guild_id != config.guild_id {
            return self.respond_ephemeral(ctx, command, "BIOCOM: COMMAND UNAVAILABLE.").await;
        }

        let member = command.member.as_ref();
        if member.is_none() || !Self::has_any_role(member.unwrap(), &config.zeus_roles()) {
            return self
                .respond_ephemeral(ctx, command, "BIOCOM: UNAUTHORIZED OPERATOR. ACCESS DENIED.")
                .await;
        }

        let options = &command.data.options();
        let message = options
            .iter()
            .find(|o| o.name == "message")
            .and_then(|o| match &o.value {
                ResolvedValue::String(s) => Some(s.to_string()),
                _ => None,
            })
            .unwrap_or_default();

        let target_channel = options
            .iter()
            .find(|o| o.name == "channel")
            .and_then(|o| match &o.value {
                ResolvedValue::Channel(c) => Some(c.id),
                _ => None,
            })
            .unwrap_or(command.channel_id);

        // Defer response
        command
            .create_response(
                &ctx.http,
                CreateInteractionResponse::Defer(
                    CreateInteractionResponseMessage::new().ephemeral(true),
                ),
            )
            .await?;

        // Send the intercepted message
        target_channel.say(&ctx.http, &message).await?;

        command
            .edit_response(
                &ctx.http,
                EditInteractionResponse::new()
                    .content(format!("BIOCOM: TRANSMISSION COMPLETE. TARGET: <#{}>", target_channel)),
            )
            .await?;

        Ok(())
    }

    async fn handle_upload_mission(
        &self,
        ctx: &Context,
        command: &CommandInteraction,
    ) -> Result<()> {
        let config = self.config.get().await;
        let guild_id: u64 = command.guild_id.map(|g| g.get()).unwrap_or(0);

        if guild_id != config.guild_id {
            return self.respond_ephemeral(ctx, command, "BIOCOM: COMMAND UNAVAILABLE.").await;
        }

        let member = command.member.as_ref();
        if member.is_none() || !Self::has_any_role(member.unwrap(), &config.zeus_roles()) {
            return self
                .respond_ephemeral(ctx, command, "BIOCOM: UNAUTHORIZED OPERATOR. ACCESS DENIED.")
                .await;
        }

        // Defer
        command
            .create_response(
                &ctx.http,
                CreateInteractionResponse::Defer(
                    CreateInteractionResponseMessage::new().ephemeral(true),
                ),
            )
            .await?;

        let attachment = command
            .data
            .resolved
            .attachments
            .values()
            .next();

        let attachment = match attachment {
            Some(a) => a,
            None => {
                return self
                    .edit_response(ctx, command, "BIOCOM: NO FILE ATTACHED.")
                    .await;
            }
        };

        if !attachment.filename.ends_with(".pbo") {
            return self
                .edit_response(ctx, command, "BIOCOM: INVALID FILE TYPE. REQUIRES .pbo")
                .await;
        }

        let safe_name = Self::sanitize_filename(&attachment.filename);
        if safe_name.is_empty() {
            return self
                .edit_response(ctx, command, "BIOCOM: INVALID FILENAME.")
                .await;
        }

        let missions_dir = format!("{}/missions", config.upload_base);
        fs::create_dir_all(&missions_dir).await?;

        let dest_path = format!("{}/{}", missions_dir, safe_name);

        // Download file
        let bytes = attachment.download().await?;
        fs::write(&dest_path, bytes).await?;

        self.edit_response(
            ctx,
            command,
            &format!("BIOCOM: MISSION `{}` UPLOADED SUCCESSFULLY.", safe_name),
        )
        .await
    }

    async fn handle_upload_preset(
        &self,
        ctx: &Context,
        command: &CommandInteraction,
    ) -> Result<()> {
        let config = self.config.get().await;
        let guild_id: u64 = command.guild_id.map(|g| g.get()).unwrap_or(0);

        if guild_id != config.guild_id {
            return self.respond_ephemeral(ctx, command, "BIOCOM: COMMAND UNAVAILABLE.").await;
        }

        let member = command.member.as_ref();
        if member.is_none() || !Self::has_any_role(member.unwrap(), &config.zeus_roles()) {
            return self
                .respond_ephemeral(ctx, command, "BIOCOM: UNAUTHORIZED OPERATOR. ACCESS DENIED.")
                .await;
        }

        // Defer
        command
            .create_response(
                &ctx.http,
                CreateInteractionResponse::Defer(
                    CreateInteractionResponseMessage::new().ephemeral(true),
                ),
            )
            .await?;

        let attachment = command
            .data
            .resolved
            .attachments
            .values()
            .next();

        let attachment = match attachment {
            Some(a) => a,
            None => {
                return self
                    .edit_response(ctx, command, "BIOCOM: NO FILE ATTACHED.")
                    .await;
            }
        };

        if !attachment.filename.ends_with(".html") {
            return self
                .edit_response(ctx, command, "BIOCOM: INVALID FILE TYPE. REQUIRES .html")
                .await;
        }

        let safe_name = Self::sanitize_filename(&attachment.filename);
        if safe_name.is_empty() {
            return self
                .edit_response(ctx, command, "BIOCOM: INVALID FILENAME.")
                .await;
        }

        let presets_dir = format!("{}/presets", config.upload_base);
        fs::create_dir_all(&presets_dir).await?;

        let dest_path = format!("{}/{}", presets_dir, safe_name);

        // Download file
        let bytes = attachment.download().await?;
        fs::write(&dest_path, bytes).await?;

        self.edit_response(
            ctx,
            command,
            &format!("BIOCOM: PRESET `{}` STORED AND BROADCAST.", safe_name),
        )
        .await
    }

    async fn handle_containers(&self, ctx: &Context, command: &CommandInteraction) -> Result<()> {
        let config = self.config.get().await;
        let guild_id: u64 = command.guild_id.map(|g| g.get()).unwrap_or(0);

        if guild_id != config.guild_id {
            return self.respond_ephemeral(ctx, command, "BIOCOM: COMMAND UNAVAILABLE.").await;
        }

        let member = command.member.as_ref();
        if member.is_none() || !Self::has_any_role(member.unwrap(), &config.admin_roles()) {
            return self
                .respond_ephemeral(ctx, command, "BIOCOM: UNAUTHORIZED OPERATOR. ACCESS DENIED.")
                .await;
        }

        // Defer
        command
            .create_response(
                &ctx.http,
                CreateInteractionResponse::Defer(
                    CreateInteractionResponseMessage::new().ephemeral(true),
                ),
            )
            .await?;

        let docker = match &self.docker {
            Some(d) => d,
            None => {
                return self
                    .edit_response(
                        ctx,
                        command,
                        "BIOCOM: DOCKER ACCESS FAILURE.\nNo container runtime connected.",
                    )
                    .await;
            }
        };

        let containers = match docker.list_running().await {
            Ok(c) => c,
            Err(e) => {
                error!("Failed to list containers: {}", e);
                return self
                    .edit_response(ctx, command, &format!("BIOCOM: DOCKER ACCESS FAILURE.\n{}", e))
                    .await;
            }
        };

        if containers.is_empty() {
            return self
                .edit_response(ctx, command, "BIOCOM: NO ACTIVE CONTAINERS DETECTED.")
                .await;
        }

        let lines: Vec<String> = containers
            .iter()
            .map(|c| format!("• `{}` — `{}` — `{}`", c.name, c.image, c.status))
            .collect();

        let mut output = lines.join("\n");
        if output.len() > 1900 {
            output.truncate(1900);
            output.push_str("\n…truncated");
        }

        self.edit_response(
            ctx,
            command,
            &format!("BIOCOM: ACTIVE CONTAINERS\n{}", output),
        )
        .await
    }

    async fn handle_logs(&self, ctx: &Context, command: &CommandInteraction) -> Result<()> {
        let config = self.config.get().await;
        let guild_id: u64 = command.guild_id.map(|g| g.get()).unwrap_or(0);

        if guild_id != config.guild_id {
            return self.respond_ephemeral(ctx, command, "BIOCOM: COMMAND UNAVAILABLE.").await;
        }

        let member = command.member.as_ref();
        if member.is_none() || !Self::has_any_role(member.unwrap(), &config.admin_roles()) {
            return self
                .respond_ephemeral(ctx, command, "BIOCOM: UNAUTHORIZED OPERATOR. ACCESS DENIED.")
                .await;
        }

        let options = &command.data.options();
        let container_name = options
            .iter()
            .find(|o| o.name == "container")
            .and_then(|o| match &o.value {
                ResolvedValue::String(s) => Some(s.to_string()),
                _ => None,
            })
            .unwrap_or_default();

        let filter_pattern = options
            .iter()
            .find(|o| o.name == "filter")
            .and_then(|o| match &o.value {
                ResolvedValue::String(s) => Some(s.to_string()),
                _ => None,
            });

        // Defer
        command
            .create_response(
                &ctx.http,
                CreateInteractionResponse::Defer(
                    CreateInteractionResponseMessage::new().ephemeral(true),
                ),
            )
            .await?;

        let docker = match &self.docker {
            Some(d) => d,
            None => {
                return self
                    .edit_response(
                        ctx,
                        command,
                        "BIOCOM: DOCKER ACCESS FAILURE.\nNo container runtime connected.",
                    )
                    .await;
            }
        };

        let logs = match docker.container_logs(&container_name, Some(500)).await {
            Ok(l) => l,
            Err(e) => {
                error!("Failed to get logs: {}", e);
                return self
                    .edit_response(ctx, command, &format!("BIOCOM: LOG RETRIEVAL FAILED.\n{}", e))
                    .await;
            }
        };

        let filtered = if let Some(pattern) = filter_pattern {
            match Regex::new(&pattern) {
                Ok(re) => logs
                    .lines()
                    .filter(|line| re.is_match(line))
                    .collect::<Vec<_>>()
                    .join("\n"),
                Err(e) => {
                    return self
                        .edit_response(
                            ctx,
                            command,
                            &format!("BIOCOM: INVALID REGEX PATTERN.\n{}", e),
                        )
                        .await;
                }
            }
        } else {
            logs
        };

        if filtered.is_empty() {
            return self
                .edit_response(ctx, command, "BIOCOM: NO MATCHING LOG ENTRIES.")
                .await;
        }

        // If logs are short, send as message; otherwise as file
        if filtered.len() <= 1900 {
            self.edit_response(
                ctx,
                command,
                &format!("BIOCOM: LOGS FOR `{}`\n```\n{}\n```", container_name, filtered),
            )
            .await
        } else {
            command
                .edit_response(
                    &ctx.http,
                    EditInteractionResponse::new()
                        .content(format!("BIOCOM: LOGS FOR `{}`", container_name))
                        .new_attachment(CreateAttachment::bytes(
                            filtered.into_bytes(),
                            format!("{}_logs.txt", container_name),
                        )),
                )
                .await?;
            Ok(())
        }
    }

    async fn respond_ephemeral(
        &self,
        ctx: &Context,
        command: &CommandInteraction,
        content: &str,
    ) -> Result<()> {
        command
            .create_response(
                &ctx.http,
                CreateInteractionResponse::Message(
                    CreateInteractionResponseMessage::new()
                        .content(content)
                        .ephemeral(true),
                ),
            )
            .await?;
        Ok(())
    }

    async fn edit_response(
        &self,
        ctx: &Context,
        command: &CommandInteraction,
        content: &str,
    ) -> Result<()> {
        command
            .edit_response(&ctx.http, EditInteractionResponse::new().content(content))
            .await?;
        Ok(())
    }
}

#[async_trait]
impl EventHandler for Bot {
    async fn ready(&self, ctx: Context, ready: Ready) {
        let runtime = self
            .docker
            .as_ref()
            .map(|d| d.runtime())
            .unwrap_or("unknown");

        info!(
            runtime = runtime,
            user = ready.user.name,
            user_id = %ready.user.id,
            "BIOCOM attached"
        );

        // Set presence
        ctx.set_presence(
            Some(serenity::all::ActivityData::playing(
                "CLCTR MULTITHREAD PROCESSOR ACTIVATED",
            )),
            serenity::all::OnlineStatus::DoNotDisturb,
        );

        // Register commands
        let config = self.config.get().await;
        let guild_id = GuildId::new(config.guild_id);

        if let Err(e) = self.register_commands(&ctx.http, guild_id).await {
            error!("Failed to register commands: {}", e);
        } else {
            info!("Commands registered");
        }
    }

    async fn interaction_create(&self, ctx: Context, interaction: Interaction) {
        if let Interaction::Command(command) = interaction {
            let result = match command.data.name.as_str() {
                "ping" => self.handle_ping(&ctx, &command).await,
                "intercept" => self.handle_intercept(&ctx, &command).await,
                "upload_mission" => self.handle_upload_mission(&ctx, &command).await,
                "upload_preset" => self.handle_upload_preset(&ctx, &command).await,
                "containers" => self.handle_containers(&ctx, &command).await,
                "logs" => self.handle_logs(&ctx, &command).await,
                _ => {
                    warn!("Unknown command: {}", command.data.name);
                    Ok(())
                }
            };

            if let Err(e) = result {
                error!(
                    command = command.data.name,
                    error = %e,
                    "Command failed"
                );
            }
        }
    }
}
