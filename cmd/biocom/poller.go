package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/skua/biocom/internal/config"
	"github.com/skua/biocom/internal/store"
)

var levelEmoji = map[string]string{
	"red":    "\U0001f534",
	"yellow": "\U0001f7e1",
	"green":  "\U0001f7e2",
}

// pollAlerts polls the SQLite store for unsent alerts and delivers them to Discord.
func pollAlerts(ctx context.Context, st *store.Store, session *discordgo.Session, cfg *config.Config, logger *slog.Logger) {
	const pollInterval = 3 * time.Second

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	logger.Info("Alert poller started", "interval", pollInterval)

	for {
		select {
		case <-ctx.Done():
			logger.Info("Alert poller stopped")
			return
		case <-ticker.C:
			alerts, err := st.UnsentAlerts(ctx)
			if err != nil {
				logger.Error("Failed to fetch unsent alerts", "error", err)
				continue
			}

			for _, a := range alerts {
				wdCfg := cfg.Watchdog()
				if wdCfg.AlertChannelID == "" {
					logger.Warn("No alert channel configured, skipping alert", "id", a.ID)
					continue
				}

				emoji := levelEmoji[a.Level]
				if emoji == "" {
					emoji = "\u26aa"
				}

				rolePing := ""
				if a.RolePing && wdCfg.AlertRoleID != "" {
					rolePing = fmt.Sprintf("<@&%s> ", wdCfg.AlertRoleID)
				}

				msg := fmt.Sprintf("%s\u26a0\ufe0f **BIOCOM WATCHDOG**\n%s **%s:** `%s`\n%s\n\u2014 %s",
					rolePing, emoji, a.Title, a.Container, a.Body,
					a.CreatedAt.Format(time.RFC3339))

				if len(msg) > 1900 {
					msg = msg[:1900] + "\n\u2026truncated"
				}

				_, err := session.ChannelMessageSend(wdCfg.AlertChannelID, msg)
				if err != nil {
					logger.Error("Failed to send alert to Discord", "id", a.ID, "error", err)
					continue // retry next poll
				}

				if err := st.MarkSent(ctx, a.ID); err != nil {
					logger.Error("Failed to mark alert as sent", "id", a.ID, "error", err)
				}
			}
		}
	}
}
