package main

import (
	"os"

	"github.com/bwmarrin/discordgo"
)

func handleHelpCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	embed := &discordgo.MessageEmbed{
		Title:       "🤖 Blazium Bot Commands",
		Description: "Here are all the available slash commands:",
		Color:       0x00ff00,
		Fields: []*discordgo.MessageEmbedField{
			{
				Name:   "`/help`",
				Value:  "Shows this help message with all available commands",
				Inline: false,
			},
			{
				Name:   "`/test`",
				Value:  "Simple test command that responds with a test message",
				Inline: false,
			},
			{
				Name:   "`/crash`",
				Value:  "Staff-only crash report tools: list, show, download, analyze",
				Inline: false,
			},
		},
		Footer: &discordgo.MessageEmbedFooter{
			Text: "Blazium Bot - Use slash commands to interact with the bot",
		},
	}

	err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Embeds: []*discordgo.MessageEmbed{embed},
		},
	})
	if err != nil {
		appLogger.Errorf("Error responding to help command: %v", err)
	}
}

func handleTestCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: "✅ Test successful! The bot is working correctly.",
		},
	})
	if err != nil {
		appLogger.Errorf("Error responding to test command: %v", err)
	}
}

func hasAdminPermissions(s *discordgo.Session, i *discordgo.InteractionCreate) bool {
	if i.Member == nil {
		appLogger.Debug("hasAdminPermissions: Member is nil")
		return false
	}

	userID := i.Member.User.ID
	username := i.Member.User.Username
	guildID := i.GuildID

	directPermissions := i.Member.Permissions
	hasDirectAdmin := directPermissions&discordgo.PermissionAdministrator != 0
	if hasDirectAdmin {
		return true
	}

	userRoles := i.Member.Roles
	if len(userRoles) == 0 {
		return false
	}

	guild, err := s.Guild(guildID)
	if err != nil {
		appLogger.Errorf("hasAdminPermissions: Error getting guild information for permission check: %v", err)
		return false
	}

	for _, roleID := range userRoles {
		for _, role := range guild.Roles {
			if role.ID == roleID && role.Permissions&discordgo.PermissionAdministrator != 0 {
				appLogger.Infof("hasAdminPermissions: User %s has admin permission through role %s", username, role.Name)
				return true
			}
		}
	}

	appLogger.Infof("hasAdminPermissions: User %s (%s) does not have administrator permission", username, userID)
	return false
}

func hasStaffAccess(s *discordgo.Session, i *discordgo.InteractionCreate) bool {
	if hasAdminPermissions(s, i) {
		return true
	}
	staffRoleID := os.Getenv("STAFF_ROLE_ID")
	if staffRoleID == "" || i.Member == nil {
		return false
	}
	for _, roleID := range i.Member.Roles {
		if roleID == staffRoleID {
			return true
		}
	}
	return false
}
