/*
Package discord_slash_commands provides a comprehensive, reusable Discord slash command system.

This package offers a complete solution for managing Discord slash commands with features like:
- Command registration and management
- Permission system
- Error handling and recovery
- Sequential command processing
- Comprehensive logging
- Easy integration with existing projects

Author: Extracted from pokeworld-bot project
License: MIT
*/

package main

import (
	"fmt"
	"log"
	"runtime/debug"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/servusdei2018/shards/v2"
)

// ============================================================================
// CORE TYPES AND STRUCTURES
// ============================================================================

// SlashCommandHandler represents a slash command handler with all necessary metadata
type SlashCommandHandler struct {
	Name        string                                                     // Command name (e.g., "help")
	Description string                                                     // Command description shown to users
	Options     []*discordgo.ApplicationCommandOption                      // Command options/parameters
	Handler     func(s *discordgo.Session, i *discordgo.InteractionCreate) // Function to execute when command is used
	Permissions []string                                                   // Required permissions ("admin", "moderator", "user", etc.)
	GuildOnly   bool                                                       // Whether command only works in guilds (not DMs)
}

// SlashCommandManager manages all slash commands for a Discord bot
type SlashCommandManager struct {
	commands           map[string]*SlashCommandHandler // Map of command name to handler
	brokenCommands     map[string]bool                 // Track commands that are currently broken
	brokenMutex        sync.RWMutex                    // Mutex for thread-safe access to broken commands
	config             *CommandConfig                  // Configuration for the command system
	logger             Logger                          // Logger interface for consistent logging
	permissionChecker  PermissionChecker               // Permission checker for command access control
	shardManager       *shards.Manager                 // Shard manager for multi-shard support
	readyShards        map[int]bool                    // Track which shards are ready
	shardsMutex        sync.RWMutex                    // Mutex for thread-safe access to shard state
	commandsRegistered bool                            // Track if commands have been registered
	registrationMutex  sync.Mutex                      // Mutex for command registration
	isShuttingDown     bool                            // Track if the bot is shutting down
	shutdownMutex      sync.RWMutex                    // Mutex for shutdown state
}

// CommandConfig holds configuration for the slash command system
type CommandConfig struct {
	GlobalCommands bool          // Whether to register commands globally
	GuildID        string        // Specific guild ID for guild-only commands
	RateLimitDelay time.Duration // Delay between command registrations to avoid rate limits
	CommandTimeout time.Duration // Timeout for command execution
	ShardCount     int           // Number of shards to use (0 = auto)
	ShardTimeout   time.Duration // Timeout for shard operations
}

// ShardManagerConfig holds configuration for the shard manager
type ShardManagerConfig struct {
	Token          string        // Discord bot token
	ShardCount     int           // Number of shards (0 = auto)
	ShardTimeout   time.Duration // Timeout for shard operations
	ReconnectDelay time.Duration // Delay between reconnection attempts
	MaxReconnects  int           // Maximum number of reconnection attempts
	CommandTimeout time.Duration // Timeout for command execution
	GlobalCommands bool          // Whether to register commands globally
	GuildID        string        // Specific guild ID for guild-only commands
}

// Logger interface for consistent logging across the system
type Logger interface {
	Info(msg string, args ...interface{})
	Warn(msg string, args ...interface{})
	Error(msg string, args ...interface{})
	Debug(msg string, args ...interface{})
}

// DefaultLogger provides a simple implementation of the Logger interface
type DefaultLogger struct{}

func (l *DefaultLogger) Info(msg string, args ...interface{}) {
	log.Printf("[INFO] "+msg, args...)
}

func (l *DefaultLogger) Warn(msg string, args ...interface{}) {
	log.Printf("[WARN] "+msg, args...)
}

func (l *DefaultLogger) Error(msg string, args ...interface{}) {
	log.Printf("[ERROR] "+msg, args...)
}

func (l *DefaultLogger) Debug(msg string, args ...interface{}) {
	log.Printf("[DEBUG] "+msg, args...)
}

// ============================================================================
// SLASH COMMAND MANAGER CONSTRUCTOR AND METHODS
// ============================================================================

// NewSlashCommandManager creates a new slash command manager with default configuration
func NewSlashCommandManager(logger Logger) *SlashCommandManager {
	if logger == nil {
		logger = &DefaultLogger{}
	}

	return &SlashCommandManager{
		commands:       make(map[string]*SlashCommandHandler),
		brokenCommands: make(map[string]bool),
		readyShards:    make(map[int]bool),
		config: &CommandConfig{
			GlobalCommands: false, // Disabled - register commands to each server individually
			RateLimitDelay: 200 * time.Millisecond,
			CommandTimeout: 30 * time.Second,
			ShardCount:     0, // Auto-detect
			ShardTimeout:   30 * time.Second,
		},
		logger: logger,
	}
}

// NewSlashCommandManagerWithConfig creates a new slash command manager with custom configuration
func NewSlashCommandManagerWithConfig(config *CommandConfig, logger Logger) *SlashCommandManager {
	if logger == nil {
		logger = &DefaultLogger{}
	}

	if config == nil {
		config = &CommandConfig{
			GlobalCommands: false, // Disabled - register commands to each server individually
			RateLimitDelay: 200 * time.Millisecond,
			CommandTimeout: 30 * time.Second,
			ShardCount:     0, // Auto-detect
			ShardTimeout:   30 * time.Second,
		}
	}

	return &SlashCommandManager{
		commands:       make(map[string]*SlashCommandHandler),
		brokenCommands: make(map[string]bool),
		readyShards:    make(map[int]bool),
		config:         config,
		logger:         logger,
	}
}

// NewSlashCommandManagerWithShards creates a new slash command manager with shard support
func NewSlashCommandManagerWithShards(shardConfig *ShardManagerConfig, logger Logger) (*SlashCommandManager, error) {
	if logger == nil {
		logger = &DefaultLogger{}
	}

	if shardConfig == nil {
		return nil, fmt.Errorf("shard configuration cannot be nil")
	}

	if shardConfig.Token == "" {
		return nil, fmt.Errorf("discord token is required")
	}

	// Set defaults for missing values
	if shardConfig.ShardTimeout == 0 {
		shardConfig.ShardTimeout = 30 * time.Second
	}
	if shardConfig.ReconnectDelay == 0 {
		shardConfig.ReconnectDelay = 5 * time.Second
	}
	if shardConfig.MaxReconnects == 0 {
		shardConfig.MaxReconnects = 10
	}
	if shardConfig.CommandTimeout == 0 {
		shardConfig.CommandTimeout = 30 * time.Second
	}

	// Create shard manager
	shardManager, err := shards.New("Bot " + shardConfig.Token)
	if err != nil {
		return nil, fmt.Errorf("failed to create shard manager: %w", err)
	}

	// Set shard count if specified
	if shardConfig.ShardCount > 0 {
		shardManager.ShardCount = shardConfig.ShardCount
	}

	manager := &SlashCommandManager{
		commands:       make(map[string]*SlashCommandHandler),
		brokenCommands: make(map[string]bool),
		readyShards:    make(map[int]bool),
		config: &CommandConfig{
			GlobalCommands: shardConfig.GlobalCommands,
			GuildID:        shardConfig.GuildID,
			RateLimitDelay: 200 * time.Millisecond,
			CommandTimeout: shardConfig.CommandTimeout,
			ShardCount:     shardConfig.ShardCount,
			ShardTimeout:   shardConfig.ShardTimeout,
		},
		logger:       logger,
		shardManager: shardManager,
	}

	return manager, nil
}

// ============================================================================
// COMMAND REGISTRATION AND MANAGEMENT
// ============================================================================

// RegisterCommand adds a new slash command to the manager
// Usage: manager.RegisterCommand("help", &CommandHandler{...})
func (m *SlashCommandManager) RegisterCommand(name string, handler *SlashCommandHandler) error {
	m.logger.Info("🔧 REGISTERING COMMAND: %s", name)
	m.logger.Info("Command Handler Details: %+v", handler)

	if name == "" {
		m.logger.Error("❌ Command name cannot be empty")
		return fmt.Errorf("command name cannot be empty")
	}

	if handler == nil {
		m.logger.Error("❌ Command handler cannot be nil")
		return fmt.Errorf("command handler cannot be nil")
	}

	if handler.Handler == nil {
		m.logger.Error("❌ Command handler function cannot be nil")
		return fmt.Errorf("command handler function cannot be nil")
	}

	// Set the name if not already set
	if handler.Name == "" {
		handler.Name = name
		m.logger.Info("📝 Set command name to: %s", name)
	}

	// Validate command
	if err := m.validateCommand(name, handler); err != nil {
		m.logger.Error("❌ Command validation failed for %s: %v", name, err)
		return fmt.Errorf("command validation failed: %w", err)
	}

	m.commands[name] = handler
	m.logger.Info("✅ Successfully registered command: %s", name)
	m.logger.Info("📊 Total registered commands: %d", len(m.commands))
	return nil
}

// RegisterCommands registers multiple commands at once
// Usage: manager.RegisterCommands(map[string]*SlashCommandHandler{...})
func (m *SlashCommandManager) RegisterCommands(commands map[string]*SlashCommandHandler) error {
	for name, handler := range commands {
		if err := m.RegisterCommand(name, handler); err != nil {
			return fmt.Errorf("failed to register command %s: %w", name, err)
		}
	}
	return nil
}

// UnregisterCommand removes a command from the manager
func (m *SlashCommandManager) UnregisterCommand(name string) {
	delete(m.commands, name)
	m.logger.Info("Unregistered command: %s", name)
}

// GetCommand retrieves a command handler by name
func (m *SlashCommandManager) GetCommand(name string) (*SlashCommandHandler, bool) {
	handler, exists := m.commands[name]
	return handler, exists
}

// ListCommands returns a list of all registered command names
func (m *SlashCommandManager) ListCommands() []string {
	var names []string
	for name := range m.commands {
		names = append(names, name)
	}
	return names
}

// ============================================================================
// DISCORD INTEGRATION METHODS
// ============================================================================

// RegisterWithDiscord registers all commands with Discord's API
// This should be called after all commands are registered
func (m *SlashCommandManager) RegisterWithDiscord(session *discordgo.Session) error {
	if session == nil {
		return fmt.Errorf("discord session cannot be nil")
	}

	if session.State == nil || session.State.User == nil {
		return fmt.Errorf("discord session not properly initialized")
	}

	m.logger.Info("Starting command registration with Discord...")

	// Validate all commands before registration
	if err := m.validateAllCommands(); err != nil {
		return fmt.Errorf("command validation failed: %w", err)
	}

	// Build command slice for Discord API
	commands := m.BuildCommandSlice()

	// Register commands based on configuration
	if m.config.GlobalCommands {
		return m.registerGlobalCommands(session, commands)
	} else if m.config.GuildID != "" {
		return m.registerGuildCommands(session, commands)
	} else {
		return fmt.Errorf("no registration scope configured (global or guild)")
	}
}

// UnregisterFromDiscord removes all commands from Discord's API
func (m *SlashCommandManager) UnregisterFromDiscord(session *discordgo.Session) error {
	if session == nil {
		return fmt.Errorf("discord session cannot be nil")
	}

	m.logger.Info("Unregistering all commands from Discord...")

	// Create empty slice to delete all commands
	emptyCommands := []*discordgo.ApplicationCommand{}

	if m.config.GlobalCommands {
		_, err := session.ApplicationCommandBulkOverwrite(session.State.User.ID, "", emptyCommands)
		if err != nil {
			return fmt.Errorf("failed to unregister global commands: %w", err)
		}
		m.logger.Info("Unregistered global commands")
	} else if m.config.GuildID != "" {
		_, err := session.ApplicationCommandBulkOverwrite(session.State.User.ID, m.config.GuildID, emptyCommands)
		if err != nil {
			return fmt.Errorf("failed to unregister guild commands: %w", err)
		}
		m.logger.Info("Unregistered guild commands for guild %s", m.config.GuildID)
	}

	return nil
}

// HandleInteraction processes Discord slash command interactions
// This should be called from your bot's interaction handler
func (m *SlashCommandManager) HandleInteraction(session *discordgo.Session, interaction *discordgo.InteractionCreate) {
	m.logger.Info("=== INTERACTION RECEIVED ===")
	m.logger.Info("Interaction Type: %d (%s)", interaction.Type, interactionTypeToString(interaction.Type))
	m.logger.Info("Interaction ID: %s", interaction.ID)
	m.logger.Info("Guild ID: %s", interaction.GuildID)
	m.logger.Info("Channel ID: %s", interaction.ChannelID)

	// Log user information
	if interaction.Member != nil {
		m.logger.Info("User: %s#%s (%s)", interaction.Member.User.Username, interaction.Member.User.Discriminator, interaction.Member.User.ID)
	} else if interaction.User != nil {
		m.logger.Info("User: %s#%s (%s)", interaction.User.Username, interaction.User.Discriminator, interaction.User.ID)
	} else {
		m.logger.Warn("No user information available in interaction")
	}

	// Only handle ApplicationCommand interactions (slash commands)
	if interaction.Type != discordgo.InteractionApplicationCommand {
		m.logger.Info("Non-slash command interaction, delegating to shoyo handler...")
		// For other interaction types (buttons, modals), delegate to the appropriate handler
		handleShoyoInteraction(session, interaction)
		return
	}

	// Log command data
	commandData := interaction.ApplicationCommandData()
	m.logger.Info("Command Data - Name: %s", commandData.Name)
	m.logger.Info("Command Data - ID: %s", commandData.ID)
	m.logger.Info("Command Data - Options Count: %d", len(commandData.Options))

	commandName := commandData.Name

	// Log all registered commands for debugging
	registeredCommands := m.ListCommands()
	m.logger.Info("Registered commands: %v", registeredCommands)
	m.logger.Info("Total registered commands: %d", len(registeredCommands))

	// Log interaction
	m.logger.Info("Processing slash command: %s from user %s in guild %s", commandName, interaction.Member.User.Username, interaction.GuildID)

	// Get command handler
	handler, exists := m.GetCommand(commandName)
	if !exists {
		m.logger.Error("❌ UNKNOWN COMMAND: %s", commandName)
		m.logger.Error("Available commands: %v", registeredCommands)
		m.sendErrorResponse(session, interaction, "Unknown command.")
		return
	}

	m.logger.Info("✅ Found command handler for: %s", commandName)

	// Check if command is broken
	if m.isCommandBroken(commandName) {
		m.logger.Warn("Command %s is currently broken", commandName)
		m.sendErrorResponse(session, interaction, "This command is currently disabled due to technical issues.")
		return
	}

	// Check permissions
	if !m.checkPermissions(interaction, handler.Permissions) {
		m.logger.Info("Permission denied for command %s from user %s", commandName, interaction.Member.User.Username)
		m.sendErrorResponse(session, interaction, "You don't have permission to use this command.")
		return
	}

	m.logger.Info("✅ Permission check passed for command %s from user %s", commandName, interaction.Member.User.Username)

	// Execute command with timeout and error handling
	m.logger.Info("🚀 Executing command: %s", commandName)
	m.executeCommand(session, interaction, handler)
	m.logger.Info("=== INTERACTION PROCESSING COMPLETE ===")
}

// interactionTypeToString converts interaction type to string for logging
func interactionTypeToString(interactionType discordgo.InteractionType) string {
	switch interactionType {
	case discordgo.InteractionPing:
		return "Ping"
	case discordgo.InteractionApplicationCommand:
		return "ApplicationCommand"
	case discordgo.InteractionMessageComponent:
		return "MessageComponent"
	case discordgo.InteractionApplicationCommandAutocomplete:
		return "ApplicationCommandAutocomplete"
	case discordgo.InteractionModalSubmit:
		return "ModalSubmit"
	default:
		return "Unknown"
	}
}

// ============================================================================
// SHARD MANAGER INTEGRATION METHODS
// ============================================================================

// StartShards starts the shard manager and registers event handlers
func (m *SlashCommandManager) StartShards() error {
	if m.shardManager == nil {
		return fmt.Errorf("shard manager not initialized")
	}

	m.logger.Info("Starting shard manager...")

	// Register event handlers
	m.shardManager.AddHandler(m.onConnect)
	m.shardManager.AddHandler(m.onReady)
	m.shardManager.AddHandler(m.onDisconnect)
	m.shardManager.AddHandler(m.onInteractionCreate)

	// Register intents
	m.shardManager.RegisterIntent(discordgo.IntentsGuildMessages | discordgo.IntentsGuilds | discordgo.IntentsDirectMessages)

	// Start the shard manager
	err := m.shardManager.Start()
	if err != nil {
		return fmt.Errorf("failed to start shard manager: %w", err)
	}

	m.logger.Info("Shard manager started successfully")
	return nil
}

// StopShards gracefully stops the shard manager
func (m *SlashCommandManager) StopShards() error {
	if m.shardManager == nil {
		return fmt.Errorf("shard manager not initialized")
	}

	m.logger.Info("Stopping shard manager...")

	// Mark as shutting down to prevent auto-reconnection
	m.setShuttingDown(true)

	// Clean up ALL commands (global and guild-specific) before shutdown
	session := m.shardManager.SessionForDM()
	if session != nil {
		m.logger.Info("Cleaning up all commands before shutdown...")
		if err := m.CleanupAllCommands(session); err != nil {
			m.logger.Warn("Failed to cleanup all commands during shutdown: %v", err)
		} else {
			m.logger.Info("Successfully cleaned up all commands before shutdown")
		}
	}

	// Shutdown the shard manager
	m.shardManager.Shutdown()

	m.logger.Info("Shard manager stopped")
	return nil
}

// setShuttingDown sets the shutdown state
func (m *SlashCommandManager) setShuttingDown(shuttingDown bool) {
	m.shutdownMutex.Lock()
	m.isShuttingDown = shuttingDown
	m.shutdownMutex.Unlock()
	m.logger.Info("Shutdown state set to: %v", shuttingDown)
}

// getShuttingDown checks if the bot is shutting down
func (m *SlashCommandManager) getShuttingDown() bool {
	m.shutdownMutex.RLock()
	defer m.shutdownMutex.RUnlock()
	return m.isShuttingDown
}

// RegisterWithShards registers commands with Discord using the shard manager
func (m *SlashCommandManager) RegisterWithShards() error {
	if m.shardManager == nil {
		return fmt.Errorf("shard manager not initialized")
	}

	// Wait for all shards to be ready
	if err := m.waitForAllShardsReady(); err != nil {
		return fmt.Errorf("failed to wait for shards: %w", err)
	}

	// Get a session to register commands
	session := m.shardManager.SessionForDM()
	if session == nil {
		return fmt.Errorf("no session available for command registration")
	}

	// Register commands using the session
	return m.RegisterWithDiscord(session)
}

// UnregisterFromShards unregisters commands from Discord using the shard manager
func (m *SlashCommandManager) UnregisterFromShards() error {
	if m.shardManager == nil {
		return fmt.Errorf("shard manager not initialized")
	}

	// Get a session to unregister commands
	session := m.shardManager.SessionForDM()
	if session == nil {
		return fmt.Errorf("no session available for command unregistration")
	}

	// Clean up ALL commands (global and guild-specific) using the session
	return m.CleanupAllCommands(session)
}

// GetShardManager returns the underlying shard manager
func (m *SlashCommandManager) GetShardManager() *shards.Manager {
	return m.shardManager
}

// GetReadyShards returns a list of ready shard IDs
func (m *SlashCommandManager) GetReadyShards() []int {
	m.shardsMutex.RLock()
	defer m.shardsMutex.RUnlock()

	var readyShards []int
	for shardID := range m.readyShards {
		readyShards = append(readyShards, shardID)
	}
	return readyShards
}

// IsShardReady checks if a specific shard is ready
func (m *SlashCommandManager) IsShardReady(shardID int) bool {
	m.shardsMutex.RLock()
	defer m.shardsMutex.RUnlock()
	return m.readyShards[shardID]
}

// GetShardCount returns the total number of shards
func (m *SlashCommandManager) GetShardCount() int {
	if m.shardManager == nil {
		return 0
	}
	return m.shardManager.ShardCount
}

// GetReadyShardCount returns the number of ready shards
func (m *SlashCommandManager) GetReadyShardCount() int {
	m.shardsMutex.RLock()
	defer m.shardsMutex.RUnlock()
	return len(m.readyShards)
}

// waitForAllShardsReady waits for all shards to be ready
func (m *SlashCommandManager) waitForAllShardsReady() error {
	timeout := time.After(m.config.ShardTimeout)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-timeout:
			return fmt.Errorf("timeout waiting for shards to be ready")
		case <-ticker.C:
			m.shardsMutex.RLock()
			readyCount := len(m.readyShards)
			totalShards := m.GetShardCount()
			m.shardsMutex.RUnlock()

			if totalShards > 0 && readyCount >= totalShards {
				m.logger.Info("All %d shards are ready", totalShards)
				return nil
			}

			m.logger.Debug("Waiting for shards: %d/%d ready", readyCount, totalShards)
		}
	}
}

// ============================================================================
// SHARD EVENT HANDLERS
// ============================================================================

// onConnect handles shard connection events
func (m *SlashCommandManager) onConnect(s *discordgo.Session, evt *discordgo.Connect) {
	m.logger.Info("Shard #%d connected", s.ShardID)

	// Update activity to show shard connection progress
	m.shardsMutex.Lock()
	connectedShards := len(m.readyShards)
	totalShards := s.ShardCount
	m.shardsMutex.Unlock()

	// Safety check for totalShards
	if totalShards <= 0 {
		totalShards = 1 // Default to 1 if not set
	}

	activity := &discordgo.Activity{
		Name: fmt.Sprintf("Connecting Shards (%d/%d)", connectedShards, totalShards),
		Type: discordgo.ActivityTypeWatching,
	}

	s.UpdateStatusComplex(discordgo.UpdateStatusData{
		Activities: []*discordgo.Activity{activity},
		Status:     "dnd", // Do not disturb while connecting
	})

	// If this is Shard #0 reconnecting, check if commands need re-registration (only if not shutting down)
	if s.ShardID == 0 && !m.getShuttingDown() {
		m.logger.Info("Shard #0 reconnected - checking if commands need re-registration...")

		// Wait a moment for the session to fully initialize
		time.Sleep(3 * time.Second)

		// Always force-update commands by removing and re-registering them (commented out - using guild-specific registration)
		/*
			m.logger.Info("Force-updating commands - removing and re-registering...")

			// First, clean up existing commands
			if err := m.CleanupAllCommands(s); err != nil {
				m.logger.Warn("Failed to cleanup commands before re-registration: %v", err)
			}

			// Wait a moment for cleanup to complete
			time.Sleep(1 * time.Second)

			// Re-register commands
			func() {
				defer func() {
					if r := recover(); r != nil {
						m.logger.Error("Panic during command re-registration on reconnect: %v", r)
						m.logger.Error("Stack trace: %s", debug.Stack())
					}
				}()

				if err := m.RegisterWithDiscord(s); err != nil {
					m.logger.Error("Failed to re-register commands: %v", err)
				} else {
					m.logger.Info("Successfully force-updated all commands")
				}
			}()
		*/
		m.logger.Info("Skipping global command force-update - using guild-specific registration")
	} else if s.ShardID == 0 && m.getShuttingDown() {
		m.logger.Info("Shard #0 reconnected but bot is shutting down - skipping command re-registration")
	}
}

// onReady handles shard ready events
func (m *SlashCommandManager) onReady(s *discordgo.Session, evt *discordgo.Ready) {
	m.logger.Info("Shard #%d ready", s.ShardID)

	// Set initial activity on first shard
	if s.ShardID == 0 {
		activity := &discordgo.Activity{
			Name: "Starting Up...",
			Type: discordgo.ActivityTypeWatching,
		}
		s.UpdateStatusComplex(discordgo.UpdateStatusData{
			Activities: []*discordgo.Activity{activity},
			Status:     "dnd",
		})
		m.logger.Info("Set initial startup activity")
	}

	// Track this shard as ready
	m.shardsMutex.Lock()
	m.readyShards[s.ShardID] = true
	totalShards := s.ShardCount
	readyCount := len(m.readyShards)
	m.shardsMutex.Unlock()

	// Safety check for totalShards
	if totalShards <= 0 {
		totalShards = 1 // Default to 1 if not set
	}

	m.logger.Info("Shard #%d ready. Progress: %d/%d shards ready", s.ShardID, readyCount, totalShards)

	// Update activity to show shard readiness progress
	activity := &discordgo.Activity{
		Name: fmt.Sprintf("Initializing Shards (%d/%d)", readyCount, totalShards),
		Type: discordgo.ActivityTypeWatching,
	}

	s.UpdateStatusComplex(discordgo.UpdateStatusData{
		Activities: []*discordgo.Activity{activity},
		Status:     "idle", // Idle while initializing
	})

	// Check if all shards are ready and commands haven't been registered yet
	m.registrationMutex.Lock()
	allReady := readyCount >= totalShards && !m.commandsRegistered
	if allReady {
		m.commandsRegistered = true
	}
	m.registrationMutex.Unlock()

	if allReady {
		// All shards are ready (commented out global command cleanup - using guild-specific registration)
		m.logger.Info("All %d shards are now ready! Using guild-specific command registration...", totalShards)

		// Skip global command cleanup and registration
		/*
			// Set activity to show cleanup
			activity := &discordgo.Activity{
				Name: "Cleaning Up Commands...",
				Type: discordgo.ActivityTypeWatching,
			}
			s.UpdateStatusComplex(discordgo.UpdateStatusData{
				Activities: []*discordgo.Activity{activity},
				Status:     "dnd", // Do not disturb while cleaning
			})

			// Clean up ALL existing commands (global and guild-specific)
			if err := m.CleanupAllCommands(s); err != nil {
				m.logger.Warn("Failed to clean up all commands: %v", err)
			}

			// Update activity to show command registration
			activity = &discordgo.Activity{
				Name: "Registering Commands...",
				Type: discordgo.ActivityTypeWatching,
			}
			s.UpdateStatusComplex(discordgo.UpdateStatusData{
				Activities: []*discordgo.Activity{activity},
				Status:     "dnd", // Do not disturb while registering
			})

			// Wait a moment to ensure stability
			m.logger.Info("Waiting before registration...")
			time.Sleep(2 * time.Second)

			// Force-register commands with panic recovery (commented out - using guild-specific registration)
			func() {
				defer func() {
					if r := recover(); r != nil {
						m.logger.Error("Panic during command registration: %v", r)
						m.logger.Error("Stack trace: %s", debug.Stack())
					}
				}()

				m.logger.Info("Force-registering commands (updating existing ones)...")
				if err := m.RegisterWithDiscord(s); err != nil {
					m.logger.Error("Failed to register commands: %v", err)
				} else {
					m.logger.Info("Successfully force-registered all commands")
				}
			}()
		*/

		// Set final "Online and Playable" status
		activity = &discordgo.Activity{
			Name: "is Online and Playable",
			Type: discordgo.ActivityTypeGame,
		}
		s.UpdateStatusComplex(discordgo.UpdateStatusData{
			Activities: []*discordgo.Activity{activity},
			Status:     "online", // Online and ready
		})

		m.logger.Info("Bot is now Online and Playable! Commands cleaned up and registered.")
	} else {
		m.logger.Info("Shard #%d - Waiting for remaining shards to be ready (%d/%d)", s.ShardID, readyCount, totalShards)
	}
}

// onDisconnect handles shard disconnection events
func (m *SlashCommandManager) onDisconnect(s *discordgo.Session, evt *discordgo.Disconnect) {
	m.logger.Warn("Shard #%d disconnected", s.ShardID)

	// If this is Shard #0 (primary shard), attempt to re-register commands (only if not shutting down)
	if s.ShardID == 0 && !m.getShuttingDown() {
		m.logger.Info("Shard #0 disconnected - attempting to re-register slash commands...")

		// Wait a moment for potential reconnection
		time.Sleep(2 * time.Second)

		// Check if the session is still valid
		if s.State != nil && s.State.User != nil {
			m.logger.Info("Shard #0 session still valid - re-registering commands...")

			// Re-register commands using the current session (commented out - using guild-specific registration)
			/*
				func() {
					defer func() {
						if r := recover(); r != nil {
							m.logger.Error("Panic during command re-registration: %v", r)
							m.logger.Error("Stack trace: %s", debug.Stack())
						}
					}()

					if err := m.RegisterWithDiscord(s); err != nil {
						m.logger.Error("Failed to re-register commands: %v", err)
					}
				}()
			*/
			m.logger.Info("Skipping global command re-registration - using guild-specific registration")
		} else {
			m.logger.Warn("Shard #0 session invalid - cannot re-register commands")
		}
	} else if s.ShardID == 0 && m.getShuttingDown() {
		m.logger.Info("Shard #0 disconnected but bot is shutting down - skipping command re-registration")
	}

	// Remove this shard from the ready shards map
	m.shardsMutex.Lock()
	delete(m.readyShards, s.ShardID)
	m.shardsMutex.Unlock()

	m.logger.Info("Shard #%d removed from ready shards map", s.ShardID)
}

// onInteractionCreate handles interaction events from all shards
func (m *SlashCommandManager) onInteractionCreate(s *discordgo.Session, i *discordgo.InteractionCreate) {
	m.logger.Info("🎯 INTERACTION RECEIVED FROM DISCORD")
	m.logger.Info("Session Info - Bot User ID: %s", s.State.User.ID)
	m.logger.Info("Session Info - Bot Username: %s", s.State.User.Username)
	m.logger.Info("Session Info - Shard ID: %d", s.ShardID)

	// Delegate to the main interaction handler
	m.HandleInteraction(s, i)
}

// cleanupExistingCommands removes all existing commands
func (m *SlashCommandManager) cleanupExistingCommands(session *discordgo.Session) error {
	if session == nil || session.State == nil || session.State.User == nil {
		return fmt.Errorf("no valid session available for cleanup")
	}

	m.logger.Info("Cleaning up existing slash commands...")

	// Helper to delete commands for a scope (global or guild)
	deleteScope := func(guildID string) (deleted, errors int) {
		cmds, err := session.ApplicationCommands(session.State.User.ID, guildID)
		if err != nil {
			m.logger.Error("Failed to get commands for guild '%s': %v", guildID, err)
			return 0, 1
		}
		if len(cmds) == 0 {
			if guildID == "" {
				m.logger.Info("No global commands to clean up")
			} else {
				m.logger.Info("No commands to clean up in guild %s", guildID)
			}
			return 0, 0
		}
		for _, cmd := range cmds {
			err := session.ApplicationCommandDelete(session.State.User.ID, guildID, cmd.ID)
			if err != nil {
				m.logger.Error("Failed to delete command '%s' (guild '%s'): %v", cmd.Name, guildID, err)
				errors++
			} else {
				if guildID == "" {
					m.logger.Info("Deleted global command: %s", cmd.Name)
				} else {
					m.logger.Info("Deleted guild command: %s (guild %s)", cmd.Name, guildID)
				}
				deleted++
			}
			time.Sleep(75 * time.Millisecond)
		}
		return
	}

	deletedCount, errorCount := 0, 0
	// Always clean global if GLOBAL true, otherwise only target the configured guild
	if m.config.GlobalCommands {
		d, e := deleteScope("")
		deletedCount += d
		errorCount += e
	} else if m.config.GuildID != "" {
		d, e := deleteScope(m.config.GuildID)
		deletedCount += d
		errorCount += e
	} else {
		m.logger.Warn("GLOBAL=false and DISCORD_SERVER_ID not set; skipping cleanup")
	}

	m.logger.Info("Command cleanup complete - Deleted: %d, Errors: %d", deletedCount, errorCount)
	return nil
}

// ============================================================================
// PERMISSION SYSTEM
// ============================================================================

// PermissionChecker defines the interface for checking user permissions
type PermissionChecker interface {
	HasPermission(userID string, permission string) (bool, error)
}

// DefaultPermissionChecker provides a simple permission checker
type DefaultPermissionChecker struct {
	adminUsers map[string]bool // Map of user IDs who are admins
}

// NewDefaultPermissionChecker creates a new default permission checker
func NewDefaultPermissionChecker(adminUsers []string) *DefaultPermissionChecker {
	adminMap := make(map[string]bool)
	for _, userID := range adminUsers {
		adminMap[userID] = true
	}
	return &DefaultPermissionChecker{adminUsers: adminMap}
}

// HasPermission checks if a user has a specific permission
func (p *DefaultPermissionChecker) HasPermission(userID string, permission string) (bool, error) {
	switch permission {
	case "admin":
		return p.adminUsers[userID], nil
	case "moderator":
		// Add your moderator logic here
		return false, nil
	case "user":
		// All users have basic user permissions
		return true, nil
	default:
		return false, fmt.Errorf("unknown permission: %s", permission)
	}
}

// SetPermissionChecker sets a custom permission checker
func (m *SlashCommandManager) SetPermissionChecker(checker PermissionChecker) {
	m.permissionChecker = checker
}

// ============================================================================
// HELPER METHODS AND UTILITIES
// ============================================================================

// validateCommand validates a single command
func (m *SlashCommandManager) validateCommand(name string, handler *SlashCommandHandler) error {
	if handler.Name == "" {
		return fmt.Errorf("command name is required")
	}

	if handler.Description == "" {
		return fmt.Errorf("command description is required")
	}

	if handler.Handler == nil {
		return fmt.Errorf("command handler function is required")
	}

	// Check for duplicate names
	for existingName, existingHandler := range m.commands {
		if existingName != name && existingHandler.Name == handler.Name {
			return fmt.Errorf("duplicate command name: %s", handler.Name)
		}
	}

	return nil
}

// validateAllCommands validates all registered commands
func (m *SlashCommandManager) validateAllCommands() error {
	for name, handler := range m.commands {
		if err := m.validateCommand(name, handler); err != nil {
			return fmt.Errorf("command %s validation failed: %w", name, err)
		}
	}
	return nil
}

// BuildCommandSlice creates a slice of Discord application commands
func (m *SlashCommandManager) BuildCommandSlice() []*discordgo.ApplicationCommand {
	commands := make([]*discordgo.ApplicationCommand, 0, len(m.commands))
	for _, handler := range m.commands {
		commands = append(commands, &discordgo.ApplicationCommand{
			Name:        handler.Name,
			Description: handler.Description,
			Options:     handler.Options,
		})
	}
	return commands
}

// registerGlobalCommands registers commands globally
func (m *SlashCommandManager) registerGlobalCommands(session *discordgo.Session, commands []*discordgo.ApplicationCommand) error {
	m.logger.Info("🌐 REGISTERING %d COMMANDS GLOBALLY", len(commands))
	m.logger.Info("Bot Application ID: %s", session.State.User.ID)

	// Log each command being registered
	for i, cmd := range commands {
		m.logger.Info("Command %d: Name='%s', Description='%s', ID='%s'", i+1, cmd.Name, cmd.Description, cmd.ID)
	}

	// Use bulk overwrite for efficiency
	registeredCommands, err := session.ApplicationCommandBulkOverwrite(session.State.User.ID, "", commands)
	if err != nil {
		m.logger.Error("❌ Failed to register global commands: %v", err)
		return fmt.Errorf("failed to register global commands: %w", err)
	}

	m.logger.Info("✅ Successfully registered %d global commands", len(registeredCommands))

	// Log the registered commands with their Discord-assigned IDs
	for _, cmd := range registeredCommands {
		m.logger.Info("✅ Registered: %s (ID: %s)", cmd.Name, cmd.ID)
	}

	return nil
}

// registerGuildCommands registers commands for a specific guild
func (m *SlashCommandManager) registerGuildCommands(session *discordgo.Session, commands []*discordgo.ApplicationCommand) error {
	m.logger.Info("Registering %d commands for guild %s", len(commands), m.config.GuildID)

	// Use bulk overwrite for efficiency
	_, err := session.ApplicationCommandBulkOverwrite(session.State.User.ID, m.config.GuildID, commands)
	if err != nil {
		return fmt.Errorf("failed to register guild commands: %w", err)
	}

	m.logger.Info("Successfully registered guild commands")
	return nil
}

// RegisterCommandsForGuild registers commands for a specific guild
func (m *SlashCommandManager) RegisterCommandsForGuild(session *discordgo.Session, guildID string) error {
	if session == nil {
		return fmt.Errorf("discord session cannot be nil")
	}

	if guildID == "" {
		return fmt.Errorf("guild ID cannot be empty")
	}

	m.logger.Info("Registering commands for guild %s", guildID)

	// Build command slice for Discord API
	commands := m.BuildCommandSlice()

	// Use bulk overwrite for efficiency
	_, err := session.ApplicationCommandBulkOverwrite(session.State.User.ID, guildID, commands)
	if err != nil {
		return fmt.Errorf("failed to register commands for guild %s: %w", guildID, err)
	}

	m.logger.Info("Successfully registered %d commands for guild %s", len(commands), guildID)
	return nil
}

// CleanupGuildCommands removes all commands from a specific guild
func (m *SlashCommandManager) CleanupGuildCommands(session *discordgo.Session, guildID string) error {
	if session == nil {
		return fmt.Errorf("discord session cannot be nil")
	}

	if guildID == "" {
		return fmt.Errorf("guild ID cannot be empty")
	}

	m.logger.Info("Cleaning up commands for guild %s", guildID)

	// Get existing commands for this guild
	commands, err := session.ApplicationCommands(session.State.User.ID, guildID)
	if err != nil {
		return fmt.Errorf("failed to get commands for guild %s: %w", guildID, err)
	}

	if len(commands) == 0 {
		m.logger.Info("No commands to clean up in guild %s", guildID)
		return nil
	}

	// Delete all commands
	deletedCount := 0
	for _, cmd := range commands {
		err := session.ApplicationCommandDelete(session.State.User.ID, guildID, cmd.ID)
		if err != nil {
			m.logger.Error("Failed to delete command '%s' from guild %s: %v", cmd.Name, guildID, err)
		} else {
			m.logger.Info("Deleted command '%s' from guild %s", cmd.Name, guildID)
			deletedCount++
		}
		// Small delay to avoid rate limits
		time.Sleep(75 * time.Millisecond)
	}

	m.logger.Info("Command cleanup complete for guild %s - Deleted: %d", guildID, deletedCount)
	return nil
}

// CleanupAllCommands removes all commands (global and guild-specific)
func (m *SlashCommandManager) CleanupAllCommands(session *discordgo.Session) error {
	if session == nil {
		return fmt.Errorf("discord session cannot be nil")
	}

	m.logger.Info("Cleaning up ALL commands (global and guild-specific)")

	totalDeleted := 0

	// Clean up global commands
	globalCommands, err := session.ApplicationCommands(session.State.User.ID, "")
	if err != nil {
		m.logger.Error("Failed to get global commands: %v", err)
	} else {
		for _, cmd := range globalCommands {
			err := session.ApplicationCommandDelete(session.State.User.ID, "", cmd.ID)
			if err != nil {
				m.logger.Error("Failed to delete global command '%s': %v", cmd.Name, err)
			} else {
				m.logger.Info("Deleted global command '%s'", cmd.Name)
				totalDeleted++
			}
			time.Sleep(75 * time.Millisecond)
		}
	}

	// Clean up guild commands for all guilds the bot is in
	for _, guild := range session.State.Guilds {
		guildCommands, err := session.ApplicationCommands(session.State.User.ID, guild.ID)
		if err != nil {
			m.logger.Error("Failed to get commands for guild %s: %v", guild.Name, err)
			continue
		}

		for _, cmd := range guildCommands {
			err := session.ApplicationCommandDelete(session.State.User.ID, guild.ID, cmd.ID)
			if err != nil {
				m.logger.Error("Failed to delete command '%s' from guild %s: %v", cmd.Name, guild.Name, err)
			} else {
				m.logger.Info("Deleted command '%s' from guild %s", cmd.Name, guild.Name)
				totalDeleted++
			}
			time.Sleep(75 * time.Millisecond)
		}
	}

	m.logger.Info("Complete command cleanup finished - Total deleted: %d", totalDeleted)
	return nil
}

// executeCommand executes a command with timeout and error handling
func (m *SlashCommandManager) executeCommand(session *discordgo.Session, interaction *discordgo.InteractionCreate, handler *SlashCommandHandler) {
	m.logger.Info("🎯 EXECUTING COMMAND: %s", handler.Name)
	m.logger.Info("Command Handler: %+v", handler)
	m.logger.Info("Command Timeout: %v", m.config.CommandTimeout)

	// Create a channel to track command completion
	done := make(chan bool, 1)

	// Execute command in a goroutine with timeout
	go func() {
		defer func() {
			if r := recover(); r != nil {
				m.logger.Error("💥 Command %s panicked: %v", handler.Name, r)
				m.logger.Error("Stack trace: %s", debug.Stack())
				m.markCommandAsBroken(handler.Name)
				done <- false
			} else {
				m.logger.Info("✅ Command %s executed successfully", handler.Name)
				done <- true
			}
		}()

		m.logger.Info("🔄 Calling command handler function for: %s", handler.Name)
		// Execute the command handler
		handler.Handler(session, interaction)
		m.logger.Info("🏁 Command handler function completed for: %s", handler.Name)
	}()

	// Wait for completion or timeout
	select {
	case success := <-done:
		if !success {
			m.logger.Error("❌ Command %s failed and was marked as broken", handler.Name)
		} else {
			m.logger.Info("✅ Command %s completed successfully", handler.Name)
		}
	case <-time.After(m.config.CommandTimeout):
		m.logger.Error("⏰ Command %s timed out after %v", handler.Name, m.config.CommandTimeout)
		m.markCommandAsBroken(handler.Name)
		m.sendErrorResponse(session, interaction, "Command timed out. Please try again later.")
	}
}

// checkPermissions checks if the user has the required permissions
func (m *SlashCommandManager) checkPermissions(interaction *discordgo.InteractionCreate, requiredPermissions []string) bool {
	m.logger.Info("checkPermissions: Checking permissions for user %s, required: %v", interaction.Member.User.Username, requiredPermissions)

	if len(requiredPermissions) == 0 {
		m.logger.Info("checkPermissions: No permissions required, allowing access")
		return true // No permissions required
	}

	// Get user ID
	var userID string
	if interaction.Member != nil {
		userID = interaction.Member.User.ID
	} else if interaction.User != nil {
		userID = interaction.User.ID
	} else {
		m.logger.Warn("checkPermissions: Cannot determine user ID")
		return false // Can't determine user
	}

	// Check each required permission
	for _, permission := range requiredPermissions {
		m.logger.Info("checkPermissions: Checking permission '%s' for user %s", permission, userID)

		if m.permissionChecker != nil {
			hasPermission, err := m.permissionChecker.HasPermission(userID, permission)
			if err != nil {
				m.logger.Error("Permission check error: %v", err)
				return false
			}
			m.logger.Info("checkPermissions: Permission '%s' result: %v", permission, hasPermission)
			if !hasPermission {
				return false
			}
		} else {
			m.logger.Info("checkPermissions: No permission checker set, using built-in logic")
			switch permission {
			case "admin":
				// Use our custom admin logic based on member and roles
				return m.checkAdminPermission(interaction)
			default:
				// For other permissions, allow by default (extend as needed)
			}
		}
	}

	m.logger.Info("checkPermissions: All permissions granted for user %s", userID)
	return true
}

// checkAdminPermission checks if the user has administrator permissions using our custom logic
func (m *SlashCommandManager) checkAdminPermission(interaction *discordgo.InteractionCreate) bool {
	// Check if user has administrator permission
	if interaction.Member == nil {
		m.logger.Debug("checkAdminPermission: Member is nil")
		return false
	}

	userID := interaction.Member.User.ID
	username := interaction.Member.User.Username
	guildID := interaction.GuildID

	m.logger.Info("checkAdminPermission: Checking permissions for user %s (%s) in guild %s", username, userID, guildID)

	// First check if the member has administrator permission directly
	directPermissions := interaction.Member.Permissions
	hasDirectAdmin := directPermissions&discordgo.PermissionAdministrator != 0
	m.logger.Info("checkAdminPermission: Direct permissions check - Permissions: %d, Has Admin: %v", directPermissions, hasDirectAdmin)

	if hasDirectAdmin {
		m.logger.Info("checkAdminPermission: User %s has direct administrator permission", username)
		return true
	}

	// If not, check all roles the user has
	userRoles := interaction.Member.Roles
	m.logger.Info("checkAdminPermission: User %s has %d roles: %v", username, len(userRoles), userRoles)

	if len(userRoles) == 0 {
		m.logger.Debug("checkAdminPermission: User %s has no roles", username)
		return false
	}

	// Get guild information to access roles
	guild, err := m.shardManager.SessionForDM().Guild(guildID)
	if err != nil {
		m.logger.Error("checkAdminPermission: Error getting guild information for permission check: %v", err)
		return false
	}

	m.logger.Info("checkAdminPermission: Guild %s has %d roles", guild.Name, len(guild.Roles))

	// Loop through all user roles to find one with admin permission
	for roleIndex, roleID := range userRoles {
		m.logger.Debug("checkAdminPermission: Checking role %d/%d: %s", roleIndex+1, len(userRoles), roleID)

		// Find the role in the guild
		roleFound := false
		for _, role := range guild.Roles {
			if role.ID == roleID {
				roleFound = true
				m.logger.Info("checkAdminPermission: Found role %s (ID: %s) with permissions: %d", role.Name, role.ID, role.Permissions)

				// Check if this role has administrator permission
				hasRoleAdmin := role.Permissions&discordgo.PermissionAdministrator != 0
				m.logger.Info("checkAdminPermission: Role %s admin check - Permissions: %d, Has Admin: %v", role.Name, role.Permissions, hasRoleAdmin)

				if hasRoleAdmin {
					m.logger.Info("checkAdminPermission: User %s has admin permission through role %s (ID: %s)", username, role.Name, role.ID)
					return true
				}
				break
			}
		}

		if !roleFound {
			m.logger.Warn("checkAdminPermission: Role %s not found in guild %s", roleID, guild.Name)
		}
	}

	m.logger.Info("checkAdminPermission: User %s does not have administrator permission through any role", username)
	return false
}

// markCommandAsBroken marks a command as broken to prevent further execution
func (m *SlashCommandManager) markCommandAsBroken(commandName string) {
	m.brokenMutex.Lock()
	m.brokenCommands[commandName] = true
	m.brokenMutex.Unlock()
	m.logger.Warn("Command %s marked as broken", commandName)
}

// isCommandBroken checks if a command is currently broken
func (m *SlashCommandManager) isCommandBroken(commandName string) bool {
	m.brokenMutex.RLock()
	defer m.brokenMutex.RUnlock()
	return m.brokenCommands[commandName]
}

// ============================================================================
// RESPONSE HELPERS
// ============================================================================

// sendErrorResponse sends an error response to the user
func (m *SlashCommandManager) sendErrorResponse(session *discordgo.Session, interaction *discordgo.InteractionCreate, message string) {
	responseData := &discordgo.InteractionResponseData{
		Content: message,
		Flags:   discordgo.MessageFlagsEphemeral, // Make error messages ephemeral (only visible to user)
	}

	session.InteractionRespond(interaction.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: responseData,
	})
}

// sendSuccessResponse sends a success response to the user
func (m *SlashCommandManager) sendSuccessResponse(session *discordgo.Session, interaction *discordgo.InteractionCreate, message string, ephemeral bool) {
	responseData := &discordgo.InteractionResponseData{
		Content: message,
	}

	if ephemeral {
		responseData.Flags = discordgo.MessageFlagsEphemeral
	}

	session.InteractionRespond(interaction.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: responseData,
	})
}

// sendEmbedResponse sends an embed response to the user
func (m *SlashCommandManager) sendEmbedResponse(session *discordgo.Session, interaction *discordgo.InteractionCreate, embed *discordgo.MessageEmbed, ephemeral bool) {
	responseData := &discordgo.InteractionResponseData{
		Embeds: []*discordgo.MessageEmbed{embed},
	}

	if ephemeral {
		responseData.Flags = discordgo.MessageFlagsEphemeral
	}

	session.InteractionRespond(interaction.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: responseData,
	})
}

// ============================================================================
// OPTION PARSING HELPERS
// ============================================================================

// GetStringOption extracts a string option from an interaction
func GetStringOption(interaction *discordgo.InteractionCreate, name string) string {
	// Check if this is a subcommand
	if len(interaction.ApplicationCommandData().Options) > 0 &&
		interaction.ApplicationCommandData().Options[0].Type == discordgo.ApplicationCommandOptionSubCommand {
		// Look in the subcommand options
		for _, option := range interaction.ApplicationCommandData().Options[0].Options {
			if option.Name == name {
				return option.StringValue()
			}
		}
	} else {
		// Look in the main command options
		for _, option := range interaction.ApplicationCommandData().Options {
			if option.Name == name {
				return option.StringValue()
			}
		}
	}
	return ""
}

// GetIntOption extracts an integer option from an interaction
func GetIntOption(interaction *discordgo.InteractionCreate, name string) int {
	// Check if this is a subcommand
	if len(interaction.ApplicationCommandData().Options) > 0 &&
		interaction.ApplicationCommandData().Options[0].Type == discordgo.ApplicationCommandOptionSubCommand {
		// Look in the subcommand options
		for _, option := range interaction.ApplicationCommandData().Options[0].Options {
			if option.Name == name {
				return int(option.IntValue())
			}
		}
	} else {
		// Look in the main command options
		for _, option := range interaction.ApplicationCommandData().Options {
			if option.Name == name {
				return int(option.IntValue())
			}
		}
	}
	return 0
}

// GetUserOption extracts a user option from an interaction
func GetUserOption(interaction *discordgo.InteractionCreate, name string) *discordgo.User {
	// Check if this is a subcommand
	if len(interaction.ApplicationCommandData().Options) > 0 &&
		interaction.ApplicationCommandData().Options[0].Type == discordgo.ApplicationCommandOptionSubCommand {
		// Look in the subcommand options
		for _, option := range interaction.ApplicationCommandData().Options[0].Options {
			if option.Name == name {
				return option.UserValue(nil)
			}
		}
	} else {
		// Look in the main command options
		for _, option := range interaction.ApplicationCommandData().Options {
			if option.Name == name {
				return option.UserValue(nil)
			}
		}
	}
	return nil
}

// ============================================================================
// USAGE EXAMPLES AND INTEGRATION PATTERNS
// ============================================================================

/*
USAGE EXAMPLES:

1. Basic Setup:
```go
// Create a new command manager
manager := NewSlashCommandManager(nil) // Uses default logger

// Register a simple command
manager.RegisterCommand("ping", &SlashCommandHandler{
    Name:        "ping",
    Description: "Pong!",
    Handler: func(s *discordgo.Session, i *discordgo.InteractionCreate) {
        manager.sendSuccessResponse(s, i, "Pong!", false)
    },
})

// Register with Discord
err := manager.RegisterWithDiscord(session)
if err != nil {
    log.Fatal(err)
}
```

2. Command with Options:
```go
manager.RegisterCommand("echo", &SlashCommandHandler{
    Name:        "echo",
    Description: "Echo a message",
    Options: []*discordgo.ApplicationCommandOption{
        {
            Type:        discordgo.ApplicationCommandOptionString,
            Name:        "message",
            Description: "Message to echo",
            Required:    true,
        },
    },
    Handler: func(s *discordgo.Session, i *discordgo.InteractionCreate) {
        message := GetStringOption(i, "message")
        manager.sendSuccessResponse(s, i, message, false)
    },
})
```

3. Command with Subcommands:
```go
manager.RegisterCommand("config", &SlashCommandHandler{
    Name:        "config",
    Description: "Configure bot settings",
    Options: []*discordgo.ApplicationCommandOption{
        {
            Type:        discordgo.ApplicationCommandOptionSubCommand,
            Name:        "set",
            Description: "Set a configuration value",
            Options: []*discordgo.ApplicationCommandOption{
                {
                    Type:        discordgo.ApplicationCommandOptionString,
                    Name:        "key",
                    Description: "Configuration key",
                    Required:    true,
                },
                {
                    Type:        discordgo.ApplicationCommandOptionString,
                    Name:        "value",
                    Description: "Configuration value",
                    Required:    true,
                },
            },
        },
    },
    Handler: func(s *discordgo.Session, i *discordgo.InteractionCreate) {
        subcommand := i.ApplicationCommandData().Options[0].Name
        switch subcommand {
        case "set":
            key := GetStringOption(i, "key")
            value := GetStringOption(i, "value")
            // Handle setting configuration
            manager.sendSuccessResponse(s, i, fmt.Sprintf("Set %s to %s", key, value), false)
        }
    },
})
```

4. Command with Permissions:
```go
// Set up permission checker
permissionChecker := NewDefaultPermissionChecker([]string{"admin_user_id_1", "admin_user_id_2"})
manager.SetPermissionChecker(permissionChecker)

// Register admin-only command
manager.RegisterCommand("admin", &SlashCommandHandler{
    Name:        "admin",
    Description: "Admin only command",
    Permissions: []string{"admin"},
    Handler: func(s *discordgo.Session, i *discordgo.InteractionCreate) {
        manager.sendSuccessResponse(s, i, "Admin command executed!", false)
    },
})
```

5. Integration with Discord Bot:
```go
func main() {
    // Create Discord session
    session, err := discordgo.New("Bot " + token)
    if err != nil {
        log.Fatal(err)
    }

    // Create command manager
    manager := NewSlashCommandManager(nil)

    // Register your commands
    setupCommands(manager)

    // Register commands with Discord
    err = manager.RegisterWithDiscord(session)
    if err != nil {
        log.Fatal(err)
    }

    // Add interaction handler
    session.AddHandler(func(s *discordgo.Session, i *discordgo.InteractionCreate) {
        manager.HandleInteraction(s, i)
    })

    // Start bot
    err = session.Open()
    if err != nil {
        log.Fatal(err)
    }
    defer session.Close()

    // Keep bot running
    select {}
}

func setupCommands(manager *SlashCommandManager) {
    // Register all your commands here
    manager.RegisterCommand("help", &SlashCommandHandler{
        Name:        "help",
        Description: "Show help information",
        Handler:     handleHelp,
    })

    // ... more commands
}
```

6. Custom Configuration:
```go
config := &CommandConfig{
    GlobalCommands: false,           // Use guild-specific commands
    GuildID:        "your_guild_id", // Specific guild ID
    RateLimitDelay: 500 * time.Millisecond, // Slower rate limiting
    CommandTimeout: 60 * time.Second,       // Longer timeout
}

manager := NewSlashCommandManagerWithConfig(config, nil)
```

7. Custom Logger:
```go
type MyLogger struct{}

func (l *MyLogger) Info(msg string, args ...interface{}) {
    log.Printf("[INFO] "+msg, args...)
}

func (l *MyLogger) Warn(msg string, args ...interface{}) {
    log.Printf("[WARN] "+msg, args...)
}

func (l *MyLogger) Error(msg string, args ...interface{}) {
    log.Printf("[ERROR] "+msg, args...)
}

func (l *MyLogger) Debug(msg string, args ...interface{}) {
    log.Printf("[DEBUG] "+msg, args...)
}

manager := NewSlashCommandManager(&MyLogger{})
```

8. ShardManager Integration:
```go
// Create shard manager configuration
shardConfig := &ShardManagerConfig{
    Token:           "your_bot_token",
    ShardCount:      4,                    // Use 4 shards
    ShardTimeout:    60 * time.Second,     // 60 second timeout
    ReconnectDelay:  5 * time.Second,      // 5 second reconnect delay
    MaxReconnects:   10,                   // Max 10 reconnection attempts
    CommandTimeout:  30 * time.Second,     // 30 second command timeout
    GlobalCommands:  false,                // Disabled - register commands to each server individually
    GuildID:         "",                   // Empty for global commands
}

// Create manager with shard support
manager, err := NewSlashCommandManagerWithShards(shardConfig, nil)
if err != nil {
    log.Fatal(err)
}

// Register your commands
manager.RegisterCommand("ping", &SlashCommandHandler{
    Name:        "ping",
    Description: "Pong!",
    Handler: func(s *discordgo.Session, i *discordgo.InteractionCreate) {
        manager.sendSuccessResponse(s, i, "Pong!", false)
    },
})

// Start shards (this will handle command registration automatically)
err = manager.StartShards()
if err != nil {
    log.Fatal(err)
}

// Keep the bot running
select {}
```

9. ShardManager with Custom Configuration:
```go
// Create custom shard configuration
shardConfig := &ShardManagerConfig{
    Token:           os.Getenv("DISCORD_TOKEN"),
    ShardCount:      0,                    // Auto-detect shard count
    ShardTimeout:    120 * time.Second,    // 2 minute timeout
    ReconnectDelay:  10 * time.Second,     // 10 second reconnect delay
    MaxReconnects:   5,                    // Max 5 reconnection attempts
    CommandTimeout:  60 * time.Second,     // 1 minute command timeout
    GlobalCommands:  false,                // Use guild-specific commands
    GuildID:         "your_guild_id",      // Specific guild ID
}

// Create manager
manager, err := NewSlashCommandManagerWithShards(shardConfig, &MyLogger{})
if err != nil {
    log.Fatal(err)
}

// Register commands
setupCommands(manager)

// Start shards
err = manager.StartShards()
if err != nil {
    log.Fatal(err)
}

// Graceful shutdown
defer func() {
    if err := manager.StopShards(); err != nil {
        log.Printf("Error stopping shards: %v", err)
    }
}()
```

10. ShardManager Status Monitoring:
```go
// Monitor shard status
go func() {
    ticker := time.NewTicker(30 * time.Second)
    defer ticker.Stop()

    for range ticker.C {
        readyShards := manager.GetReadyShards()
        totalShards := manager.GetShardCount()
        readyCount := manager.GetReadyShardCount()

        log.Printf("Shard Status: %d/%d ready, Ready shards: %v",
            readyCount, totalShards, readyShards)

        // Check if specific shard is ready
        if manager.IsShardReady(0) {
            log.Println("Primary shard (0) is ready")
        }
    }
}()
```

11. Manual ShardManager Control:
```go
// Get the underlying shard manager for advanced control
shardManager := manager.GetShardManager()
if shardManager != nil {
    // Access shard manager methods directly
    // shardManager.SessionForShard(0) // Get specific shard session
    // shardManager.SessionForDM()     // Get DM session
}

// Manual command registration (after shards are ready)
err = manager.RegisterWithShards()
if err != nil {
    log.Printf("Failed to register commands: %v", err)
}

// Manual command unregistration
err = manager.UnregisterFromShards()
if err != nil {
    log.Printf("Failed to unregister commands: %v", err)
}
```

INTEGRATION PATTERNS:

1. Modular Command Registration:
```go
func RegisterUserCommands(manager *SlashCommandManager) {
    manager.RegisterCommand("profile", &CommandHandler{...})
    manager.RegisterCommand("stats", &CommandHandler{...})
}

func RegisterAdminCommands(manager *SlashCommandManager) {
    manager.RegisterCommand("ban", &CommandHandler{...})
    manager.RegisterCommand("kick", &CommandHandler{...})
}

func main() {
    manager := NewSlashCommandManager(nil)

    RegisterUserCommands(manager)
    RegisterAdminCommands(manager)

    // Register with Discord...
}
```

2. Command Middleware:
```go
func withLogging(handler func(*discordgo.Session, *discordgo.InteractionCreate)) func(*discordgo.Session, *discordgo.InteractionCreate) {
    return func(s *discordgo.Session, i *discordgo.InteractionCreate) {
        log.Printf("Executing command: %s", i.ApplicationCommandData().Name)
        handler(s, i)
        log.Printf("Command completed: %s", i.ApplicationCommandData().Name)
    }
}

manager.RegisterCommand("test", &SlashCommandHandler{
    Name:        "test",
    Description: "Test command",
    Handler:     withLogging(handleTest),
})
```

3. Error Recovery:
```go
func withRecovery(handler func(*discordgo.Session, *discordgo.InteractionCreate)) func(*discordgo.Session, *discordgo.InteractionCreate) {
    return func(s *discordgo.Session, i *discordgo.InteractionCreate) {
        defer func() {
            if r := recover(); r != nil {
                log.Printf("Command panicked: %v", r)
                // Send error response to user
                s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
                    Type: discordgo.InteractionResponseChannelMessageWithSource,
                    Data: &discordgo.InteractionResponseData{
                        Content: "An error occurred while processing your command.",
                        Flags:   discordgo.MessageFlagsEphemeral,
                    },
                })
            }
        }()
        handler(s, i)
    }
}
```

This comprehensive slash command system provides everything needed to manage Discord slash commands in any Go project, with extensive documentation and examples for easy integration.
*/
