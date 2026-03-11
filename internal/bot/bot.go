package bot

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/skua/biocom/internal/config"
	"github.com/skua/biocom/internal/docker"
)

// Bot represents the Discord bot instance.
type Bot struct {
	session      *discordgo.Session
	cfg          *config.Config
	dockerClient *docker.Client
	logger       *slog.Logger

	ctx    context.Context
	cancel context.CancelFunc

	mu       sync.RWMutex
	commands []*discordgo.ApplicationCommand
}

// New creates a new Bot instance.
func New(cfg *config.Config, dockerClient *docker.Client, logger *slog.Logger) (*Bot, error) {
	session, err := discordgo.New("Bot " + cfg.DiscordToken)
	if err != nil {
		return nil, fmt.Errorf("failed to create Discord session: %w", err)
	}

	session.Identify.Intents = discordgo.IntentsGuilds | discordgo.IntentsGuildMessages

	bot := &Bot{
		session:      session,
		cfg:          cfg,
		dockerClient: dockerClient,
		logger:       logger,
	}

	return bot, nil
}

// Start connects to Discord and registers commands.
func (b *Bot) Start(ctx context.Context) error {
	b.ctx, b.cancel = context.WithCancel(ctx)

	b.session.AddHandler(b.onReady)
	b.session.AddHandler(b.onInteraction)

	if err := b.session.Open(); err != nil {
		return fmt.Errorf("failed to open Discord session: %w", err)
	}

	b.logger.Info("Discord session opened")
	return nil
}

// Stop gracefully shuts down the bot.
func (b *Bot) Stop() error {
	b.logger.Info("Shutting down bot...")

	if b.cancel != nil {
		b.cancel()
	}

	// Unregister commands
	b.mu.RLock()
	commands := b.commands
	b.mu.RUnlock()

	for _, cmd := range commands {
		if err := b.session.ApplicationCommandDelete(b.session.State.User.ID, b.cfg.GuildID, cmd.ID); err != nil {
			b.logger.Warn("Failed to delete command", "command", cmd.Name, "error", err)
		}
	}

	return b.session.Close()
}

// onReady handles the ready event from Discord.
func (b *Bot) onReady(s *discordgo.Session, r *discordgo.Ready) {
	runtime := "unknown"
	if b.dockerClient != nil {
		runtime = b.dockerClient.Runtime()
	}

	b.logger.Info("BIOCOM attached",
		"runtime", runtime,
		"user", r.User.Username,
		"userID", r.User.ID,
	)

	// Set presence
	if err := s.UpdateStatusComplex(discordgo.UpdateStatusData{
		Status: string(discordgo.StatusDoNotDisturb),
		Activities: []*discordgo.Activity{
			{
				Name: "CLCTR MULTITHREAD PROCESSOR ACTIVATED",
				Type: discordgo.ActivityTypeGame,
			},
		},
	}); err != nil {
		b.logger.Warn("Failed to update presence", "error", err)
	}

	// Register slash commands
	if err := b.registerCommands(); err != nil {
		b.logger.Error("Failed to register commands", "error", err)
	}
}

// registerCommands registers all slash commands with Discord.
func (b *Bot) registerCommands() error {
	commands := b.getCommandDefinitions()

	registered, err := b.session.ApplicationCommandBulkOverwrite(
		b.session.State.User.ID,
		b.cfg.GuildID,
		commands,
	)
	if err != nil {
		return fmt.Errorf("failed to register commands: %w", err)
	}

	b.mu.Lock()
	b.commands = registered
	b.mu.Unlock()

	b.logger.Info("Commands registered", "count", len(registered))
	return nil
}

// onInteraction handles incoming interactions (slash commands, autocomplete, etc).
func (b *Bot) onInteraction(s *discordgo.Session, i *discordgo.InteractionCreate) {
	switch i.Type {
	case discordgo.InteractionApplicationCommand:
		data := i.ApplicationCommandData()

		ctx, cancel := context.WithTimeout(b.ctx, 30*time.Second)
		defer cancel()

		handlers := map[string]func(context.Context, *discordgo.Session, *discordgo.InteractionCreate){
			"ping":           b.handlePing,
			"intercept":      b.handleIntercept,
			"upload_mission": b.handleUploadMission,
			"upload_preset":  b.handleUploadPreset,
			"containers":     b.handleContainers,
			"logs":           b.handleLogs,
		}

		if handler, ok := handlers[data.Name]; ok {
			handler(ctx, s, i)
		} else {
			b.logger.Warn("Unknown command", "name", data.Name)
		}

	case discordgo.InteractionApplicationCommandAutocomplete:
		b.handleAutocomplete(s, i)
	}
}

// Session returns the underlying Discord session for testing.
func (b *Bot) Session() *discordgo.Session {
	return b.session
}
