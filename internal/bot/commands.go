package bot

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
)

var safeFilenameRegex = regexp.MustCompile(`[^A-Za-z0-9 ._-]`)

// getCommandDefinitions returns all slash command definitions.
func (b *Bot) getCommandDefinitions() []*discordgo.ApplicationCommand {
	return []*discordgo.ApplicationCommand{
		{
			Name:        "ping",
			Description: "Check if BIOCOM is alive",
		},
		{
			Name:        "intercept",
			Description: "Intercept communication",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "message",
					Description: "Post a message to the specified channel or thread",
					Required:    true,
				},
				{
					Type:        discordgo.ApplicationCommandOptionChannel,
					Name:        "channel",
					Description: "Optional channel or thread (defaults to current)",
					Required:    false,
				},
			},
		},
		{
			Name:        "upload_mission",
			Description: "Upload a mission (.pbo)",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionAttachment,
					Name:        "file",
					Description: "Mission file (.pbo)",
					Required:    true,
				},
			},
		},
		{
			Name:        "upload_preset",
			Description: "Upload a preset (.html)",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionAttachment,
					Name:        "file",
					Description: "Preset file (.html)",
					Required:    true,
				},
			},
		},
		{
			Name:        "containers",
			Description: "List running Docker containers on the BIOCOM stack",
		},
		{
			Name:        "logs",
			Description: "Pull container logs",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:         discordgo.ApplicationCommandOptionString,
					Name:         "container",
					Description:  "Container name",
					Required:     true,
					Autocomplete: true,
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "filter",
					Description: "Regex filter (queries all lines, returns matches)",
					Required:    false,
				},
			},
		},
	}
}

// sanitizeFilename cleans a filename for safe storage.
func sanitizeFilename(raw string) string {
	decoded, err := url.QueryUnescape(raw)
	if err != nil {
		decoded = raw
	}

	decoded = filepath.Base(decoded)
	sanitized := safeFilenameRegex.ReplaceAllString(decoded, "")
	return strings.TrimSpace(sanitized)
}

// hasAnyRoleID checks if a member holds any of the given role IDs.
func hasAnyRoleID(member *discordgo.Member, roleIDs []string) bool {
	if member == nil || len(roleIDs) == 0 {
		return false
	}
	for _, memberRole := range member.Roles {
		for _, allowed := range roleIDs {
			if memberRole == allowed {
				return true
			}
		}
	}
	return false
}

// respondEphemeral sends an ephemeral response.
func respondEphemeral(s *discordgo.Session, i *discordgo.InteractionCreate, content string) {
	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: content,
			Flags:   discordgo.MessageFlagsEphemeral,
		},
	})
}

// deferEphemeral defers the response as ephemeral.
func deferEphemeral(s *discordgo.Session, i *discordgo.InteractionCreate) {
	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Flags: discordgo.MessageFlagsEphemeral,
		},
	})
}

// followupEphemeral edits the deferred response with content.
func followupEphemeral(s *discordgo.Session, i *discordgo.InteractionCreate, content string) {
	s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
		Content: &content,
	})
}

// handlePing handles the /ping command.
func (b *Bot) handlePing(_ context.Context, s *discordgo.Session, i *discordgo.InteractionCreate) {
	respondEphemeral(s, i, "BIOCOM: STATUS OPERATIONAL. GLOBAL LOOP.")
}

// handleIntercept handles the /intercept command.
func (b *Bot) handleIntercept(_ context.Context, s *discordgo.Session, i *discordgo.InteractionCreate) {
	deferEphemeral(s, i)

	data := i.ApplicationCommandData()

	var message string
	var channelID string

	for _, opt := range data.Options {
		switch opt.Name {
		case "message":
			message = opt.StringValue()
		case "channel":
			channelID = opt.ChannelValue(s).ID
		}
	}

	if channelID == "" {
		channelID = i.ChannelID
	}

	if !hasAnyRoleID(i.Member, b.cfg.Roles().Zeus) {
		followupEphemeral(s, i, MsgUnauthorized)
		return
	}

	_, err := s.ChannelMessageSend(channelID, message)
	if err != nil {
		b.logger.Error("Failed to send intercept message", "error", err, "channelID", channelID)
		followupEphemeral(s, i, "BIOCOM: CANNOT TRANSMIT TO TARGET.")
		return
	}

	followupEphemeral(s, i, "BIOCOM: MESSAGE SENT.")
}

// uploadParams describes one kind of file upload.
type uploadParams struct {
	extension string // e.g. ".pbo", ".html"
	directory string // save directory path
	noun      string // e.g. "MISSION", "PRESET"
	logLabel  string // e.g. "Mission upload"
}

// handleUpload is the shared implementation for file upload commands.
func (b *Bot) handleUpload(_ context.Context, s *discordgo.Session, i *discordgo.InteractionCreate, p uploadParams) {
	deferEphemeral(s, i)

	if !hasAnyRoleID(i.Member, b.cfg.Roles().Zeus) {
		followupEphemeral(s, i, MsgUnauthorized)
		return
	}

	data := i.ApplicationCommandData()

	var attachmentID string
	for _, opt := range data.Options {
		if opt.Name == "file" {
			attachmentID = opt.Value.(string)
		}
	}

	attachment, ok := data.Resolved.Attachments[attachmentID]
	if !ok {
		followupEphemeral(s, i, MsgAttachmentMissing)
		return
	}

	b.logger.Info(p.logLabel,
		"user", i.Member.User.Username,
		"filename", attachment.Filename,
		"size", attachment.Size,
	)

	if !strings.HasSuffix(strings.ToLower(attachment.Filename), p.extension) {
		followupEphemeral(s, i, fmt.Sprintf("BIOCOM: INVALID FILE TYPE. EXPECTED `%s`.", p.extension))
		return
	}

	safeName := sanitizeFilename(attachment.Filename)
	if !strings.HasSuffix(strings.ToLower(safeName), p.extension) {
		followupEphemeral(s, i, MsgInvalidFilename)
		return
	}

	savePath := filepath.Join(p.directory, safeName)
	if err := downloadFile(attachment.URL, savePath); err != nil {
		b.logger.Error("Failed to download file", "error", err)
		followupEphemeral(s, i, MsgDownloadFailure)
		return
	}

	file, err := os.Open(savePath)
	if err != nil {
		b.logger.Error("Failed to open saved file", "error", err)
		followupEphemeral(s, i, MsgFileAccessFailure)
		return
	}
	defer file.Close()

	_, err = s.ChannelMessageSendComplex(i.ChannelID, &discordgo.MessageSend{
		Content: fmt.Sprintf("BIOCOM: %s INGESTED.", p.noun),
		Files:   []*discordgo.File{{Name: safeName, Reader: file}},
	})
	if err != nil {
		b.logger.Error("Failed to broadcast file", "error", err)
		followupEphemeral(s, i, MsgBroadcastFailure)
		return
	}

	followupEphemeral(s, i, fmt.Sprintf("BIOCOM: %s `%s` STORED AND BROADCAST.", p.noun, safeName))
}

// handleUploadMission handles the /upload_mission command.
func (b *Bot) handleUploadMission(ctx context.Context, s *discordgo.Session, i *discordgo.InteractionCreate) {
	b.handleUpload(ctx, s, i, uploadParams{
		extension: ".pbo",
		directory: b.cfg.MissionsDir,
		noun:      "MISSION",
		logLabel:  "Mission upload",
	})
}

// handleUploadPreset handles the /upload_preset command.
func (b *Bot) handleUploadPreset(ctx context.Context, s *discordgo.Session, i *discordgo.InteractionCreate) {
	b.handleUpload(ctx, s, i, uploadParams{
		extension: ".html",
		directory: b.cfg.PresetsDir,
		noun:      "PRESET",
		logLabel:  "Preset upload",
	})
}

// handleContainers handles the /containers command.
func (b *Bot) handleContainers(ctx context.Context, s *discordgo.Session, i *discordgo.InteractionCreate) {
	if i.GuildID != b.cfg.GuildID {
		respondEphemeral(s, i, "BIOCOM: COMMAND UNAVAILABLE.")
		return
	}

	if !hasAnyRoleID(i.Member, b.cfg.Roles().Admin) {
		respondEphemeral(s, i, MsgUnauthorized)
		return
	}

	deferEphemeral(s, i)

	if b.dockerClient == nil {
		followupEphemeral(s, i, MsgDockerNoRuntime)
		return
	}

	containers, err := b.dockerClient.ListRunning(ctx)
	if err != nil {
		b.logger.Error("Failed to list containers", "error", err)
		followupEphemeral(s, i, fmt.Sprintf("BIOCOM: DOCKER ACCESS FAILURE.\n%s", err))
		return
	}

	if len(containers) == 0 {
		followupEphemeral(s, i, "BIOCOM: NO ACTIVE CONTAINERS DETECTED.")
		return
	}

	var lines []string
	for _, c := range containers {
		lines = append(lines, fmt.Sprintf("• `%s` — `%s` — `%s`", c.Name, c.Image, c.Status))
	}

	output := strings.Join(lines, "\n")

	if len(output) > 1900 {
		output = output[:1900] + "\n…truncated"
	}

	followupEphemeral(s, i, fmt.Sprintf("BIOCOM: ACTIVE CONTAINERS\n%s", output))
}

// downloadFile downloads a file from a URL to the specified path.
func downloadFile(url, path string) error {
	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("failed to download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed with status: %d", resp.StatusCode)
	}

	out, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("failed to create file: %w", err)
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	if err != nil {
		return fmt.Errorf("failed to write file: %w", err)
	}

	return nil
}

// handleAutocomplete responds to autocomplete interactions.
func (b *Bot) handleAutocomplete(s *discordgo.Session, i *discordgo.InteractionCreate) {
	data := i.ApplicationCommandData()

	if data.Name != "logs" {
		return
	}

	var typed string
	for _, opt := range data.Options {
		if opt.Focused {
			typed = strings.ToLower(opt.StringValue())
			break
		}
	}

	ctx, cancel := context.WithTimeout(b.ctx, 5*time.Second)
	defer cancel()

	containers, err := b.dockerClient.ListRunning(ctx)
	if err != nil {
		b.logger.Error("Autocomplete: failed to list containers", "error", err)
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionApplicationCommandAutocompleteResult,
			Data: &discordgo.InteractionResponseData{},
		})
		return
	}

	var choices []*discordgo.ApplicationCommandOptionChoice
	for _, c := range containers {
		if typed == "" || strings.Contains(strings.ToLower(c.Name), typed) {
			choices = append(choices, &discordgo.ApplicationCommandOptionChoice{
				Name:  c.Name,
				Value: c.Name,
			})
		}
		if len(choices) >= 25 {
			break
		}
	}

	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionApplicationCommandAutocompleteResult,
		Data: &discordgo.InteractionResponseData{
			Choices: choices,
		},
	})
}

// handleLogs handles the /logs command.
func (b *Bot) handleLogs(ctx context.Context, s *discordgo.Session, i *discordgo.InteractionCreate) {
	if !hasAnyRoleID(i.Member, b.cfg.Roles().Admin) {
		respondEphemeral(s, i, MsgUnauthorized)
		return
	}

	deferEphemeral(s, i)

	if b.dockerClient == nil {
		followupEphemeral(s, i, MsgDockerNoRuntime)
		return
	}

	data := i.ApplicationCommandData()

	var containerName, filter string
	for _, opt := range data.Options {
		switch opt.Name {
		case "container":
			containerName = opt.StringValue()
		case "filter":
			filter = opt.StringValue()
		}
	}

	if containerName == "" {
		followupEphemeral(s, i, "BIOCOM: CONTAINER NAME REQUIRED.")
		return
	}

	tail := 100
	if filter != "" {
		tail = 0
	}

	logs, err := b.dockerClient.ContainerLogs(ctx, containerName, tail)
	if err != nil {
		b.logger.Error("Failed to fetch container logs", "container", containerName, "error", err)
		followupEphemeral(s, i, fmt.Sprintf("BIOCOM: LOG RETRIEVAL FAILURE.\n%s", err))
		return
	}

	if filter != "" {
		re, err := regexp.Compile(filter)
		if err != nil {
			followupEphemeral(s, i, fmt.Sprintf("BIOCOM: INVALID REGEX.\n`%s`", err))
			return
		}

		lines := strings.Split(logs, "\n")
		var matched []string
		for _, line := range lines {
			if re.MatchString(line) {
				matched = append(matched, line)
			}
		}

		if len(matched) == 0 {
			followupEphemeral(s, i, fmt.Sprintf("BIOCOM: NO MATCHES for `%s` in `%s`.", filter, containerName))
			return
		}

		logs = strings.Join(matched, "\n")
	}

	output := fmt.Sprintf("BIOCOM: LOGS `%s`", containerName)
	if filter != "" {
		output += fmt.Sprintf(" (filter: `%s`)", filter)
	}
	output += fmt.Sprintf("\n```\n%s\n```", logs)

	if len(output) > 1900 {
		codeEnd := "\n```"
		maxContent := 1900 - len(output[:strings.Index(output, "```\n")+4]) - len(codeEnd) - 15
		if maxContent < 100 {
			maxContent = 100
		}
		if len(logs) > maxContent {
			logs = "…truncated\n" + logs[len(logs)-maxContent:]
		}
		output = fmt.Sprintf("BIOCOM: LOGS `%s`", containerName)
		if filter != "" {
			output += fmt.Sprintf(" (filter: `%s`)", filter)
		}
		output += fmt.Sprintf("\n```\n%s\n```", logs)
		if len(output) > 1900 {
			output = output[:1900] + "\n```"
		}
	}

	followupEphemeral(s, i, output)
}
