package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
)

// API structures for coupon creation
type CouponAPIRequest struct {
	CreatedBy string `json:"created_by" validate:"required"`
	Type      string `json:"type" validate:"required,oneof=page credits mcp"`
	Credits   *int   `json:"credits,omitempty"`
	Duration  *int   `json:"duration,omitempty"`   // Duration in days
	ExpiresIn *int   `json:"expires_in,omitempty"` // Days from now
}

type CouponAPIResponse struct {
	Success bool `json:"success"`
	Data    struct {
		Code      string    `json:"code"`
		Type      string    `json:"type"`
		CreatedBy string    `json:"created_by"`
		ExpiresIn time.Time `json:"expires_in"`
		Duration  *int      `json:"duration,omitempty"`
		Credits   *int      `json:"credits,omitempty"`
	} `json:"data"`
}

// Discord command handlers for slash commands

// getDurationFromResponse safely extracts duration from API response
func getDurationFromResponse(response *CouponAPIResponse) int {
	if response.Data.Duration == nil {
		return 0
	}
	return *response.Data.Duration
}

// getCreditsFromResponse safely extracts credits from API response
func getCreditsFromResponse(response *CouponAPIResponse) int {
	if response.Data.Credits == nil {
		return 0
	}
	return *response.Data.Credits
}

// createPageCouponAPI makes API call to create a page coupon
func createPageCouponAPI(userID, username string) (*CouponAPIResponse, error) {
	// Get API configuration from environment
	apiURL := os.Getenv("COUPON_API")
	if apiURL == "" {
		return nil, fmt.Errorf("COUPON_API environment variable not set")
	}

	// Get authorization tokens
	botToken := os.Getenv("BOT_ACCESS_TOKEN")
	botSecret := os.Getenv("BOT_SECRET_KEY")

	if botToken == "" {
		return nil, fmt.Errorf("BOT_ACCESS_TOKEN environment variable not set")
	}

	if botSecret == "" {
		return nil, fmt.Errorf("BOT_SECRET_KEY environment variable not set")
	}

	// Create request payload
	duration := 30
	expiresIn := 365
	requestData := CouponAPIRequest{
		CreatedBy: fmt.Sprintf("%s#%s", username, userID),
		Type:      "page",
		Duration:  &duration,
		ExpiresIn: &expiresIn,
	}

	// Marshal request to JSON
	jsonData, err := json.Marshal(requestData)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request data: %w", err)
	}

	appLogger.Infof("Making API call to %s with payload: %s", apiURL, string(jsonData))

	// Create HTTP request
	req, err := http.NewRequest("POST", apiURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP request: %w", err)
	}

	// Set headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+botToken)
	req.Header.Set("X-Bot-Secret", botSecret)

	// Make HTTP request
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to make HTTP request: %w", err)
	}
	defer resp.Body.Close()

	// Read response
	var response CouponAPIResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	duration = getDurationFromResponse(&response)
	appLogger.Infof("API response received - Status: %d, Success: %t, Code: %s, Duration: %d", resp.StatusCode, response.Success, response.Data.Code, duration)

	if !response.Success {
		return nil, fmt.Errorf("API returned success=false")
	}

	return &response, nil
}

// createCreditsCouponAPI makes API call to create a credits coupon
func createCreditsCouponAPI(userID, username string, creditsAmount int) (*CouponAPIResponse, error) {
	// Get API configuration from environment
	apiURL := os.Getenv("COUPON_API")
	if apiURL == "" {
		return nil, fmt.Errorf("COUPON_API environment variable not set")
	}

	// Get authorization tokens
	botToken := os.Getenv("BOT_ACCESS_TOKEN")
	botSecret := os.Getenv("BOT_SECRET_KEY")

	if botToken == "" {
		return nil, fmt.Errorf("BOT_ACCESS_TOKEN environment variable not set")
	}

	if botSecret == "" {
		return nil, fmt.Errorf("BOT_SECRET_KEY environment variable not set")
	}

	// Create request payload for credits coupon
	expiresIn := 180
	requestData := CouponAPIRequest{
		CreatedBy: fmt.Sprintf("%s#%s", username, userID),
		Type:      "credits",
		Credits:   &creditsAmount,
		ExpiresIn: &expiresIn,
		// Duration is nil for credits coupons
	}

	// Marshal request to JSON
	jsonData, err := json.Marshal(requestData)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request data: %w", err)
	}

	appLogger.Infof("Making credits coupon API call to %s with payload: %s", apiURL, string(jsonData))

	// Create HTTP request
	req, err := http.NewRequest("POST", apiURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP request: %w", err)
	}

	// Set headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+botToken)
	req.Header.Set("X-Bot-Secret", botSecret)

	// Make HTTP request
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to make HTTP request: %w", err)
	}
	defer resp.Body.Close()

	// Read response
	var response CouponAPIResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	credits := getCreditsFromResponse(&response)
	appLogger.Infof("Credits coupon API response received - Status: %d, Success: %t, Code: %s, Credits: %d",
		resp.StatusCode, response.Success, response.Data.Code, credits)

	if !response.Success {
		return nil, fmt.Errorf("API returned success=false")
	}

	return &response, nil
}

// createMCPCouponAPI makes API call to create an MCP coupon
func createMCPCouponAPI(userID, username string) (*CouponAPIResponse, error) {
	// Get API configuration from environment
	apiURL := os.Getenv("COUPON_API")
	if apiURL == "" {
		return nil, fmt.Errorf("COUPON_API environment variable not set")
	}

	// Get authorization tokens
	botToken := os.Getenv("BOT_ACCESS_TOKEN")
	botSecret := os.Getenv("BOT_SECRET_KEY")

	if botToken == "" {
		return nil, fmt.Errorf("BOT_ACCESS_TOKEN environment variable not set")
	}

	if botSecret == "" {
		return nil, fmt.Errorf("BOT_SECRET_KEY environment variable not set")
	}

	// Create request payload for MCP coupon
	duration := 90
	expiresIn := 365
	requestData := CouponAPIRequest{
		CreatedBy: fmt.Sprintf("%s#%s", username, userID),
		Type:      "mcp",
		Duration:  &duration,
		ExpiresIn: &expiresIn,
		// Credits is nil for MCP coupons
	}

	// Marshal request to JSON
	jsonData, err := json.Marshal(requestData)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request data: %w", err)
	}

	appLogger.Infof("Making MCP coupon API call to %s with payload: %s", apiURL, string(jsonData))

	// Create HTTP request
	req, err := http.NewRequest("POST", apiURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP request: %w", err)
	}

	// Set headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+botToken)
	req.Header.Set("X-Bot-Secret", botSecret)

	// Make HTTP request
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to make HTTP request: %w", err)
	}
	defer resp.Body.Close()

	// Read response
	var response CouponAPIResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	duration = getDurationFromResponse(&response)
	appLogger.Infof("MCP coupon API response received - Status: %d, Success: %t, Code: %s, Duration: %d",
		resp.StatusCode, response.Success, response.Data.Code, duration)

	if !response.Success {
		return nil, fmt.Errorf("API returned success=false")
	}

	return &response, nil
}

// handleHelpCommand handles the /help slash command
func handleHelpCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {

	// Create embed with all available commands
	embed := &discordgo.MessageEmbed{
		Title:       "🤖 Blazium Bot Commands",
		Description: "Here are all the available slash commands:",
		Color:       0x00ff00, // Green color
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
				Name:   "`/shoyo`",
				Value:  "Shoyo.work portfolio platform information and coupon management",
				Inline: false,
			},
		},
		Footer: &discordgo.MessageEmbedFooter{
			Text: "Blazium Bot - Use slash commands to interact with the bot",
		},
	}

	// Respond to the interaction
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

// handleTestCommand handles the /test slash command
func handleTestCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {

	// Respond with a simple test message
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

// hasAdminPermissions checks if the user has administrator permissions
func hasAdminPermissions(s *discordgo.Session, i *discordgo.InteractionCreate) bool {
	// Check if user has administrator permission
	if i.Member == nil {
		appLogger.Debug("hasAdminPermissions: Member is nil")
		return false
	}

	userID := i.Member.User.ID
	username := i.Member.User.Username
	guildID := i.GuildID

	appLogger.Infof("hasAdminPermissions: Checking permissions for user %s (%s) in guild %s", username, userID, guildID)

	// First check if the member has administrator permission directly
	directPermissions := i.Member.Permissions
	hasDirectAdmin := directPermissions&discordgo.PermissionAdministrator != 0
	appLogger.Debugf("hasAdminPermissions: Direct permissions check - Permissions: %s, Has Admin: %v", decodePermissions(directPermissions), hasDirectAdmin)

	if hasDirectAdmin {
		appLogger.Debugf("hasAdminPermissions: User %s has direct administrator permission", username)
		return true
	}

	// If not, check all roles the user has
	userRoles := i.Member.Roles
	appLogger.Infof("hasAdminPermissions: User %s has %d roles: %v", username, len(userRoles), userRoles)

	if len(userRoles) == 0 {
		appLogger.Debugf("hasAdminPermissions: User %s has no roles", username)
		return false
	}

	// Get guild information to access roles
	guild, err := s.Guild(guildID)
	if err != nil {
		appLogger.Errorf("hasAdminPermissions: Error getting guild information for permission check: %v", err)
		return false
	}

	appLogger.Infof("hasAdminPermissions: Guild %s has %d roles", guild.Name, len(guild.Roles))

	// Loop through all user roles to find one with admin permission
	for roleIndex, roleID := range userRoles {
		appLogger.Debugf("hasAdminPermissions: Checking role %d/%d: %s", roleIndex+1, len(userRoles), roleID)

		// Find the role in the guild
		roleFound := false
		for _, role := range guild.Roles {
			if role.ID == roleID {
				roleFound = true
				appLogger.Infof("hasAdminPermissions: Found role %s (ID: %s) with permissions: %s", role.Name, role.ID, decodePermissions(role.Permissions))

				// Check if this role has administrator permission
				hasRoleAdmin := role.Permissions&discordgo.PermissionAdministrator != 0
				appLogger.Infof("hasAdminPermissions: Role %s admin check - Permissions: %s, Has Admin: %v", role.Name, decodePermissions(role.Permissions), hasRoleAdmin)

				if hasRoleAdmin {
					appLogger.Infof("hasAdminPermissions: User %s has admin permission through role %s (ID: %s)", username, role.Name, role.ID)
					return true
				}
				break
			}
		}

		if !roleFound {
			appLogger.Warnf("hasAdminPermissions: Role %s not found in guild %s", roleID, guild.Name)
		}
	}

	appLogger.Infof("hasAdminPermissions: User %s does not have administrator permission through any role", username)
	return false
}

// decodePermissions converts a permission integer to a readable string of permission names
func decodePermissions(permissions int64) string {
	permissionNames := []string{}

	if permissions&discordgo.PermissionAdministrator != 0 {
		permissionNames = append(permissionNames, "Administrator")
	}
	if permissions&discordgo.PermissionManageServer != 0 {
		permissionNames = append(permissionNames, "ManageServer")
	}
	if permissions&discordgo.PermissionManageRoles != 0 {
		permissionNames = append(permissionNames, "ManageRoles")
	}
	if permissions&discordgo.PermissionManageChannels != 0 {
		permissionNames = append(permissionNames, "ManageChannels")
	}
	if permissions&discordgo.PermissionKickMembers != 0 {
		permissionNames = append(permissionNames, "KickMembers")
	}
	if permissions&discordgo.PermissionBanMembers != 0 {
		permissionNames = append(permissionNames, "BanMembers")
	}
	if permissions&discordgo.PermissionManageMessages != 0 {
		permissionNames = append(permissionNames, "ManageMessages")
	}
	if permissions&discordgo.PermissionManageWebhooks != 0 {
		permissionNames = append(permissionNames, "ManageWebhooks")
	}
	if permissions&discordgo.PermissionManageNicknames != 0 {
		permissionNames = append(permissionNames, "ManageNicknames")
	}
	if permissions&discordgo.PermissionManageEmojis != 0 {
		permissionNames = append(permissionNames, "ManageEmojis")
	}

	if len(permissionNames) == 0 {
		return "None"
	}

	return fmt.Sprintf("%s (0x%x)", fmt.Sprintf("%v", permissionNames), permissions)
}

// handleShoyoCommand handles the /shoyo slash command
func handleShoyoCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	// Check if user has admin or staff permissions
	hasAdmin := hasAdminPermissions(s, i)
	hasStaff := false

	// Check if user has staff role
	staffRoleID := os.Getenv("STAFF_ROLE_ID")
	if staffRoleID != "" && i.Member != nil {
		for _, roleID := range i.Member.Roles {
			if roleID == staffRoleID {
				hasStaff = true
				break
			}
		}
	}

	if hasAdmin || hasStaff {
		// Admin/Staff: Show coupon options
		handleShoyoAdminCommand(s, i)
	} else {
		// Regular user: Show Shoyo.work information
		handleShoyoUserCommand(s, i)
	}
}

// handleShoyoUserCommand shows Shoyo.work information for regular users
func handleShoyoUserCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	embed := &discordgo.MessageEmbed{
		Title:       "🚀 SHOYO.WORK - Developer Portfolio Platform",
		Description: "Create stunning, customizable portfolio pages with advanced analytics and premium features!",
		Color:       0x00ff00, // Green color
		Fields: []*discordgo.MessageEmbedField{
			{
				Name:   "🎨 **Portfolio Creation**",
				Value:  "• Create multiple portfolio pages with custom vanity URLs\n• Futuristic design with project showcases\n• Social media integration with major platforms\n• Contact forms and email collection\n• Password protection for private portfolios",
				Inline: false,
			},
			{
				Name:   "📊 **Analytics & Insights**",
				Value:  "• Comprehensive visitor tracking and analytics\n• Country-based visitor breakdown with visualizations\n• Section interaction tracking (clicks, views, engagement)\n• Password-protected page analytics\n• Downloadable reports in multiple formats",
				Inline: false,
			},
			{
				Name:   "💎 **Premium Features** ($10 USD/page/year)",
				Value:  "• Advanced analytics with API access\n• Self-hosting capabilities with Docker\n• Custom domains and white-label options\n• Full REST API access and webhook integrations\n• Enhanced data export capabilities",
				Inline: false,
			},
			{
				Name:   "🏢 **Enterprise Solutions**",
				Value:  "• Complete white-label deployments\n• Custom domain integration\n• Single Sign-On (SSO) integration\n• Custom database connections\n• 24/7 dedicated support teams",
				Inline: false,
			},
		},
		Footer: &discordgo.MessageEmbedFooter{
			Text: "Visit shoyo.work today and create your professional portfolio!",
		},
		URL: "https://shoyo.work",
	}

	// Respond to the interaction
	err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Embeds: []*discordgo.MessageEmbed{embed},
		},
	})

	if err != nil {
		appLogger.Errorf("Error responding to shoyo command: %v", err)
	}
}

// handleShoyoAdminCommand shows coupon creation options for admin/staff users
func handleShoyoAdminCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	embed := &discordgo.MessageEmbed{
		Title:       "🎫 SHOYO.WORK Coupon Management",
		Description: "Create coupons for Shoyo.work premium features",
		Color:       0xff6b35, // Orange color
		Fields: []*discordgo.MessageEmbedField{
			{
				Name:   "📄 **Page Coupons**",
				Value:  "Create coupons for premium page access",
				Inline: false,
			},
			{
				Name:   "💳 **Credit Coupons**",
				Value:  "Create coupons for platform credits (100-1000)",
				Inline: false,
			},
			{
				Name:   "🔧 **MCP Coupons**",
				Value:  "Create coupons for MCP (Model Context Protocol) features",
				Inline: false,
			},
		},
		Footer: &discordgo.MessageEmbedFooter{
			Text: "Select a coupon type to create",
		},
	}

	// Create buttons for coupon types
	components := []discordgo.MessageComponent{
		discordgo.ActionsRow{
			Components: []discordgo.MessageComponent{
				discordgo.Button{
					Label:    "📄 Page Coupon",
					Style:    discordgo.PrimaryButton,
					CustomID: "shoyo_coupon_page",
				},
				discordgo.Button{
					Label:    "💳 Credit Coupon",
					Style:    discordgo.SecondaryButton,
					CustomID: "shoyo_coupon_credits",
				},
				discordgo.Button{
					Label:    "🔧 MCP Coupon",
					Style:    discordgo.SuccessButton,
					CustomID: "shoyo_coupon_mcp",
				},
			},
		},
	}

	// Respond to the interaction
	err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Embeds:     []*discordgo.MessageEmbed{embed},
			Components: components,
			Flags:      discordgo.MessageFlagsEphemeral,
		},
	})

	if err != nil {
		appLogger.Errorf("Error responding to shoyo admin command: %v", err)
	}
}

// handleShoyoCouponInteraction handles button clicks for coupon creation
func handleShoyoCouponInteraction(s *discordgo.Session, i *discordgo.InteractionCreate) {
	customID := i.MessageComponentData().CustomID

	switch customID {
	case "shoyo_coupon_page":
		handlePageCouponCreation(s, i)
	case "shoyo_coupon_credits":
		handleCreditsCouponCreation(s, i)
	case "shoyo_coupon_mcp":
		handleMCPCouponCreation(s, i)
	}
}

// handlePageCouponCreation handles page coupon creation
func handlePageCouponCreation(s *discordgo.Session, i *discordgo.InteractionCreate) {
	appLogger.Infof("Page coupon creation requested by user %s (%s)", i.Member.User.Username, i.Member.User.ID)

	// Make API call to create the coupon
	apiResponse, err := createPageCouponAPI(i.Member.User.ID, i.Member.User.Username)
	if err != nil {
		appLogger.Errorf("Failed to create page coupon via API: %v", err)

		// Show error embed
		errorEmbed := &discordgo.MessageEmbed{
			Title:       "❌ Page Coupon Creation Failed",
			Description: "Failed to create page coupon",
			Color:       0xff0000,
			Fields: []*discordgo.MessageEmbedField{
				{
					Name:   "Error",
					Value:  fmt.Sprintf("```%v```", err),
					Inline: false,
				},
			},
		}

		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseUpdateMessage,
			Data: &discordgo.InteractionResponseData{
				Embeds:     []*discordgo.MessageEmbed{errorEmbed},
				Components: []discordgo.MessageComponent{}, // Remove buttons
			},
		})
		return
	}

	duration := getDurationFromResponse(apiResponse)
	appLogger.Infof("Page coupon created successfully - Code: %s, Duration: %d days, Expires: %s", apiResponse.Data.Code, duration, apiResponse.Data.ExpiresIn.Format(time.RFC3339))

	// Send webhook notification with API response data
	go func() {
		details := fmt.Sprintf("Premium page access coupon - Code: %s, Duration: %d days, Expires: %s",
			apiResponse.Data.Code, duration, apiResponse.Data.ExpiresIn.Format(time.RFC3339))
		if err := sendCouponWebhook("Page Coupon", i.Member.User, details); err != nil {
			appLogger.Errorf("Failed to send webhook for page coupon: %v", err)
		}
	}()

	// Create updated embed showing the selection with API response data
	embed := &discordgo.MessageEmbed{
		Title:       "🎫 Shoyo.work Coupon System",
		Description: "Coupon creation system for Shoyo.work platform",
		Color:       0x00ff00,
		Fields: []*discordgo.MessageEmbedField{
			{
				Name:   "✅ **Selection Made**",
				Value:  "**📄 Page Coupon** selected by " + i.Member.User.Mention(),
				Inline: false,
			},
			{
				Name: "📄 **Page Coupon Created**",
				Value: fmt.Sprintf("**Coupon Code:** `%s`\n**Duration:** %d days\n**Expires:** %s",
					apiResponse.Data.Code, duration, apiResponse.Data.ExpiresIn.Format(time.RFC3339)),
				Inline: false,
			},
		},
		Footer: &discordgo.MessageEmbedFooter{
			Text: "Coupon created successfully - buttons disabled",
		},
	}

	// Update the original message with no buttons (remove components)
	err = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseUpdateMessage,
		Data: &discordgo.InteractionResponseData{
			Embeds:     []*discordgo.MessageEmbed{embed},
			Components: []discordgo.MessageComponent{}, // Empty components removes buttons
		},
	})

	if err != nil {
		appLogger.Errorf("Error updating page coupon selection: %v", err)
	}
}

// handleCreditsCouponCreation handles credits coupon creation
func handleCreditsCouponCreation(s *discordgo.Session, i *discordgo.InteractionCreate) {
	appLogger.Infof("Credits coupon creation requested by user %s (%s)", i.Member.User.Username, i.Member.User.ID)

	// Create modal for credits amount input
	err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseModal,
		Data: &discordgo.InteractionResponseData{
			CustomID: "credits_coupon_modal",
			Title:    "💳 Credits Coupon Amount",
			Components: []discordgo.MessageComponent{
				discordgo.ActionsRow{
					Components: []discordgo.MessageComponent{
						discordgo.TextInput{
							CustomID:    "credits_amount",
							Label:       "Credits Amount (100-1000)",
							Style:       discordgo.TextInputShort,
							Placeholder: "Enter credits amount (e.g., 500)",
							Required:    true,
							MinLength:   3,
							MaxLength:   4,
						},
					},
				},
			},
		},
	})

	if err != nil {
		appLogger.Errorf("Error showing credits coupon modal: %v", err)
		return
	}
}

// handleMCPCouponCreation handles MCP coupon creation
func handleMCPCouponCreation(s *discordgo.Session, i *discordgo.InteractionCreate) {
	appLogger.Infof("MCP coupon creation requested by user %s (%s)", i.Member.User.Username, i.Member.User.ID)

	// Make API call to create the MCP coupon
	apiResponse, err := createMCPCouponAPI(i.Member.User.ID, i.Member.User.Username)
	if err != nil {
		appLogger.Errorf("Failed to create MCP coupon via API: %v", err)

		// Show error embed
		errorEmbed := &discordgo.MessageEmbed{
			Title:       "❌ MCP Coupon Creation Failed",
			Description: "Failed to create MCP coupon",
			Color:       0xff0000,
			Fields: []*discordgo.MessageEmbedField{
				{
					Name:   "Error",
					Value:  fmt.Sprintf("```%v```", err),
					Inline: false,
				},
			},
		}

		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseUpdateMessage,
			Data: &discordgo.InteractionResponseData{
				Embeds:     []*discordgo.MessageEmbed{errorEmbed},
				Components: []discordgo.MessageComponent{}, // Remove buttons
			},
		})
		return
	}

	duration := getDurationFromResponse(apiResponse)
	appLogger.Infof("MCP coupon created successfully - Code: %s, Duration: %d days, Expires: %s",
		apiResponse.Data.Code, duration, apiResponse.Data.ExpiresIn.Format(time.RFC3339))

	// Send webhook notification with API response data
	go func() {
		details := fmt.Sprintf("Model Context Protocol access coupon - Code: %s, Duration: %d days, Expires: %s",
			apiResponse.Data.Code, duration, apiResponse.Data.ExpiresIn.Format(time.RFC3339))
		if err := sendCouponWebhook("MCP Coupon", i.Member.User, details); err != nil {
			appLogger.Errorf("Failed to send webhook for MCP coupon: %v", err)
		}
	}()

	// Create updated embed showing the selection with API response data
	embed := &discordgo.MessageEmbed{
		Title:       "🎫 Shoyo.work Coupon System",
		Description: "Coupon creation system for Shoyo.work platform",
		Color:       0x00ff00,
		Fields: []*discordgo.MessageEmbedField{
			{
				Name:   "✅ **Selection Made**",
				Value:  "**🔧 MCP Coupon** selected by " + i.Member.User.Mention(),
				Inline: false,
			},
			{
				Name: "🔧 **MCP Coupon Created**",
				Value: fmt.Sprintf("**Coupon Code:** `%s`\n**Duration:** %d days\n**Expires:** %s",
					apiResponse.Data.Code, duration, apiResponse.Data.ExpiresIn.Format(time.RFC3339)),
				Inline: false,
			},
		},
		Footer: &discordgo.MessageEmbedFooter{
			Text: "Coupon created successfully - buttons disabled",
		},
	}

	// Update the original message with no buttons (remove components)
	err = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseUpdateMessage,
		Data: &discordgo.InteractionResponseData{
			Embeds:     []*discordgo.MessageEmbed{embed},
			Components: []discordgo.MessageComponent{}, // Empty components removes buttons
		},
	})

	if err != nil {
		appLogger.Errorf("Error updating MCP coupon selection: %v", err)
	}
}

// handleShoyoInteraction handles all shoyo-related interactions (buttons only)
func handleShoyoInteraction(s *discordgo.Session, i *discordgo.InteractionCreate) {
	switch i.Type {
	case discordgo.InteractionMessageComponent:
		// Handle button clicks
		customID := i.MessageComponentData().CustomID
		if strings.HasPrefix(customID, "shoyo_") {
			handleShoyoCouponInteraction(s, i)
		}
	case discordgo.InteractionModalSubmit:
		// Handle modal submissions
		customID := i.ModalSubmitData().CustomID
		if customID == "credits_coupon_modal" {
			handleCreditsModalSubmit(s, i)
		}
	}
}

// handleCreditsModalSubmit handles the credits coupon modal submission
func handleCreditsModalSubmit(s *discordgo.Session, i *discordgo.InteractionCreate) {
	appLogger.Infof("Credits coupon modal submitted by user %s (%s)", i.Member.User.Username, i.Member.User.ID)

	// Get the credits amount from the modal
	modalData := i.ModalSubmitData()
	creditsAmount := ""

	for _, component := range modalData.Components {
		if row, ok := component.(*discordgo.ActionsRow); ok {
			for _, comp := range row.Components {
				if textInput, ok := comp.(*discordgo.TextInput); ok {
					if textInput.CustomID == "credits_amount" {
						creditsAmount = textInput.Value
						break
					}
				}
			}
		}
	}

	// Validate credits amount
	if creditsAmount == "" {
		appLogger.Warn("Credits amount is empty")
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "❌ Credits amount cannot be empty. Please try again.",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		return
	}

	// Convert to integer and validate range
	amount, err := strconv.Atoi(creditsAmount)
	if err != nil || amount < 100 || amount > 1000 {
		appLogger.Warnf("Invalid credits amount: %s", creditsAmount)
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "❌ Credits amount must be between 100 and 1000. Please try again.",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		return
	}

	appLogger.Infof("Valid credits amount received: %d", amount)

	// Make API call to create the credits coupon
	apiResponse, err := createCreditsCouponAPI(i.Member.User.ID, i.Member.User.Username, amount)
	if err != nil {
		appLogger.Errorf("Failed to create credits coupon via API: %v", err)

		// Show error embed
		errorEmbed := &discordgo.MessageEmbed{
			Title:       "❌ Credits Coupon Creation Failed",
			Description: "Failed to create credits coupon",
			Color:       0xff0000,
			Fields: []*discordgo.MessageEmbedField{
				{
					Name:   "Error",
					Value:  fmt.Sprintf("```%v```", err),
					Inline: false,
				},
			},
		}

		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseUpdateMessage,
			Data: &discordgo.InteractionResponseData{
				Embeds:     []*discordgo.MessageEmbed{errorEmbed},
				Components: []discordgo.MessageComponent{}, // Remove buttons
			},
		})
		return
	}

	credits := getCreditsFromResponse(apiResponse)
	appLogger.Infof("Credits coupon created successfully - Code: %s, Credits: %d, Expires: %s",
		apiResponse.Data.Code, credits, apiResponse.Data.ExpiresIn.Format(time.RFC3339))

	// Create updated embed showing the selection with API response data
	embed := &discordgo.MessageEmbed{
		Title:       "🎫 Shoyo.work Coupon System",
		Description: "Coupon creation system for Shoyo.work platform",
		Color:       0x00ff00,
		Fields: []*discordgo.MessageEmbedField{
			{
				Name:   "✅ **Selection Made**",
				Value:  "**💳 Credits Coupon** selected by " + i.Member.User.Mention(),
				Inline: false,
			},
			{
				Name: "💳 **Credits Coupon Created**",
				Value: fmt.Sprintf("**Coupon Code:** `%s`\n**Credits:** %d credits\n**Expires:** %s",
					apiResponse.Data.Code, credits, apiResponse.Data.ExpiresIn.Format(time.RFC3339)),
				Inline: false,
			},
		},
		Footer: &discordgo.MessageEmbedFooter{
			Text: "Coupon created successfully - buttons disabled",
		},
	}

	// Update the original message with no buttons (remove components)
	err = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseUpdateMessage,
		Data: &discordgo.InteractionResponseData{
			Embeds:     []*discordgo.MessageEmbed{embed},
			Components: []discordgo.MessageComponent{}, // Empty components removes buttons
		},
	})

	if err != nil {
		appLogger.Errorf("Error updating credits coupon selection: %v", err)
		return
	}

	// Send webhook notification with API response data
	go func() {
		details := fmt.Sprintf("Platform credits coupon - Code: %s, Credits: %d, Expires: %s",
			apiResponse.Data.Code, credits, apiResponse.Data.ExpiresIn.Format(time.RFC3339))
		if err := sendCouponWebhook("Credits Coupon", i.Member.User, details); err != nil {
			appLogger.Errorf("Failed to send webhook for credits coupon: %v", err)
		}
	}()
}

// sendCouponWebhook sends a webhook notification for coupon creation
func sendCouponWebhook(couponType string, createdBy *discordgo.User, details string) error {
	webhookURL := os.Getenv("DISCORD_WEBHOOK")
	if webhookURL == "" {
		appLogger.Warn("DISCORD_WEBHOOK environment variable not set - skipping webhook notification")
		return nil
	}

	// Create embed for webhook
	embed := map[string]interface{}{
		"title":       "🎫 New Shoyo.work Coupon Created",
		"description": fmt.Sprintf("A new **%s** coupon has been created", couponType),
		"color":       0x00ff00, // Green color
		"fields": []map[string]interface{}{
			{
				"name":   "📋 Coupon Type",
				"value":  couponType,
				"inline": true,
			},
			{
				"name":   "👤 Created By",
				"value":  fmt.Sprintf("%s#%s\n<@%s>", createdBy.Username, createdBy.Discriminator, createdBy.ID),
				"inline": true,
			},
			{
				"name":   "🕐 Created At",
				"value":  fmt.Sprintf("<t:%d:F>", time.Now().Unix()),
				"inline": false,
			},
		},
		"footer": map[string]interface{}{
			"text": "Blazium Bot - Shoyo.work Coupon System",
		},
		"timestamp": time.Now().Format(time.RFC3339),
	}

	// Add details if provided
	if details != "" {
		fields := embed["fields"].([]map[string]interface{})
		fields = append(fields, map[string]interface{}{
			"name":   "ℹ️ Details",
			"value":  details,
			"inline": false,
		})
		embed["fields"] = fields
	}

	// Create webhook payload
	payload := map[string]interface{}{
		"embeds": []map[string]interface{}{embed},
	}

	// Marshal to JSON
	jsonData, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal webhook payload: %w", err)
	}

	// Send webhook
	resp, err := http.Post(webhookURL, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("failed to send webhook: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned non-success status code: %d", resp.StatusCode)
	}

	appLogger.Infof("Successfully sent webhook notification for %s coupon creation", couponType)
	return nil
}
