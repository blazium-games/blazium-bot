package main

import (
	"fmt"
	"strings"

	"github.com/bwmarrin/discordgo"
)

// Tickets: members press a button to open a private channel with staff.
//
//	/tickettype create|edit|staff|show|post|header|welcome|list|delete   set up ticket types (Manage Server)
//	/tickettype question add|remove|move                                optional intake questions
//	/ticket close|claim|add|remove|rename                               inside a ticket (staff; the opener can close)
//	/ticket list|panel                                                  every ticket, and a panel of ticket types and forms (Manage Server)
//
// A ticket type (Support, Bug report, Partnership…) has its own category, staff roles, log
// channel and button. It can ask intake questions first, shown as pop-ups. Opening a ticket
// makes a numbered channel (ticket-0042-ann) for the member and the type's staff, with
// Claim and Close buttons. Closing asks for a reason, locks the member out, posts a full
// transcript to the log channel (and to the member by DM), and offers Reopen and Delete.

const ticketColor = 0x5865F2

var (
	colorOpened = 0x57F287
	colorClosed = 0xED4245
	colorNotice = 0xFEE75C
)

func initTickets() {
	db := botDatabase()
	if db == nil {
		appLogger.Warn("Postgres is unavailable; tickets disabled")
		return
	}
	store, err := newPostgresTicketsStore(db)
	if err != nil {
		appLogger.Errorf("Tickets disabled: %v", err)
		return
	}
	tickets = store
	appLogger.Info("Tickets ready")
}

// ============================================================================
// COMMAND DEFINITIONS
// ============================================================================

type cmdOpts = map[string]*discordgo.ApplicationCommandInteractionDataOption

func typeOption(required bool) *discordgo.ApplicationCommandOption {
	return &discordgo.ApplicationCommandOption{
		Type: discordgo.ApplicationCommandOptionInteger, Name: "type", Description: "Which ticket type",
		Required: required, Autocomplete: true, MinValue: floatPtr(1),
	}
}

func sub(name, description string, options ...*discordgo.ApplicationCommandOption) *discordgo.ApplicationCommandOption {
	return &discordgo.ApplicationCommandOption{Type: discordgo.ApplicationCommandOptionSubCommand, Name: name, Description: description, Options: options}
}

func strOpt(name, description string, required bool, maxLength int) *discordgo.ApplicationCommandOption {
	return &discordgo.ApplicationCommandOption{Type: discordgo.ApplicationCommandOptionString, Name: name, Description: description, Required: required, MaxLength: maxLength}
}

func boolOpt(name, description string) *discordgo.ApplicationCommandOption {
	return &discordgo.ApplicationCommandOption{Type: discordgo.ApplicationCommandOptionBoolean, Name: name, Description: description}
}

func roleOpt(name, description string, required bool) *discordgo.ApplicationCommandOption {
	return &discordgo.ApplicationCommandOption{Type: discordgo.ApplicationCommandOptionRole, Name: name, Description: description, Required: required}
}

func userOpt(name, description string, required bool) *discordgo.ApplicationCommandOption {
	return &discordgo.ApplicationCommandOption{Type: discordgo.ApplicationCommandOptionUser, Name: name, Description: description, Required: required}
}

func channelOpt(name, description string, required bool, types ...discordgo.ChannelType) *discordgo.ApplicationCommandOption {
	return &discordgo.ApplicationCommandOption{Type: discordgo.ApplicationCommandOptionChannel, Name: name, Description: description, Required: required, ChannelTypes: types}
}

func intOpt(name, description string, required bool, lo, hi float64) *discordgo.ApplicationCommandOption {
	return &discordgo.ApplicationCommandOption{Type: discordgo.ApplicationCommandOptionInteger, Name: name, Description: description, Required: required, MinValue: &lo, MaxValue: hi}
}

func ticketStatusChoices() []*discordgo.ApplicationCommandOptionChoice {
	return []*discordgo.ApplicationCommandOptionChoice{
		{Name: "Open", Value: ticketOpen}, {Name: "Closed", Value: ticketClosed}, {Name: "Deleted", Value: ticketDeleted},
	}
}

func ticketTypeCommandOptions() []*discordgo.ApplicationCommandOption {
	kinds := make([]*discordgo.ApplicationCommandOptionChoice, len(formKinds))
	for n, k := range formKinds {
		kinds[n] = &discordgo.ApplicationCommandOptionChoice{Name: k.Name, Value: k.Key}
	}
	kind := strOpt("kind", "Short answer, long answer, pick one or pick any (default: short)", false, 0)
	kind.Choices = kinds
	category := []discordgo.ChannelType{discordgo.ChannelTypeGuildCategory}
	return []*discordgo.ApplicationCommandOption{
		{
			Type: discordgo.ApplicationCommandOptionSubCommandGroup, Name: "question", Description: "Questions a member answers before their ticket opens",
			Options: []*discordgo.ApplicationCommandOption{
				sub("add", "Add an intake question to the end of a ticket type",
					typeOption(true),
					strOpt("question", fmt.Sprintf("The question (up to %d characters)", formLabelMax), true, 100),
					kind,
					boolOpt("required", "Whether it must be answered (yes if empty)"),
					strOpt("help", fmt.Sprintf("A line of help under the question (up to %d characters)", formHelpMax), false, 200),
					strOpt("choices", "For pick one / pick any: the choices, separated by commas", false, 3000),
					intOpt("max_length", "For typed answers: the most characters allowed", false, 1, 1000),
				),
				sub("remove", "Remove an intake question", typeOption(true),
					intOpt("number", "The question's number, from /tickettype show", true, 1, formMaxQuestions)),
				sub("move", "Move an intake question to another place", typeOption(true),
					intOpt("number", "The question's number now", true, 1, formMaxQuestions),
					intOpt("to", "Its new number", true, 1, formMaxQuestions)),
			},
		},
		sub("create", "Make a new ticket type (Support, Bug report…)",
			strOpt("name", fmt.Sprintf("The type's name, shown on its button (up to %d characters)", formNameMax), true, formNameMax),
			channelOpt("category", "The category its ticket channels go in", true, category...),
			channelOpt("log_channel", "A staff-only channel for ticket logs and transcripts", true, panelTextChannels...),
			roleOpt("staff_role", "A staff role that sees and handles these tickets (add more with /tickettype staff)", true),
			strOpt("intro", "What the type is for, shown above its Open button", false, formIntroMax),
			strOpt("emoji", "The emoji on its button", false, 60),
			boolOpt("allow_public", "Allow a log channel everyone can see (not recommended)"),
		),
		sub("edit", "Change a ticket type. Only the options you fill in change.",
			typeOption(true),
			strOpt("name", "A new name", false, formNameMax),
			strOpt("intro", "New words above the Open button", false, formIntroMax),
			channelOpt("category", "A new category for new tickets", false, category...),
			channelOpt("log_channel", "A new staff-only log channel", false, panelTextChannels...),
			boolOpt("open", "Whether members can open new tickets of this type"),
			intOpt("max_open", "How many open tickets of this type one member may have (default 1)", false, 1, 10),
			boolOpt("dm_transcript", "Send members their transcript by DM when a ticket closes"),
			boolOpt("ping_staff", "Ping the staff roles when a ticket opens"),
			strOpt("emoji", "The emoji on its button (type none to remove it)", false, 60),
			boolOpt("allow_public", "Allow a log channel everyone can see"),
		),
		sub("staff", "Add or remove a staff role that handles this type's tickets",
			typeOption(true),
			roleOpt("add_role", "A role to add", false),
			roleOpt("remove_role", "A role to remove (from new tickets)", false),
		),
		sub("show", "A ticket type's settings and questions, with a button to try it", typeOption(true)),
		sub("post", "Post a ticket type's Open button", typeOption(true),
			channelOpt("channel", "Where to post it (this channel if left empty)", false, panelTextChannels...)),
		sub("header", "Write text shown at the top of the intake questions", typeOption(true)),
		sub("welcome", "Write your own message, posted in each new ticket", typeOption(true)),
		sub("list", "Every ticket type in this server"),
		sub("delete", "Delete a ticket type and its ticket records", typeOption(true)),
	}
}

func ticketCommandOptions() []*discordgo.ApplicationCommandOption {
	status := strOpt("status", "Only these (open if left empty)", false, 0)
	status.Choices = ticketStatusChoices()
	return []*discordgo.ApplicationCommandOption{
		sub("close", "Close this ticket", strOpt("reason", "Why it's being closed (shown in the log and the DM)", false, formReasonMax)),
		sub("claim", "Claim this ticket, or unclaim it if it's yours"),
		sub("add", "Let someone else see and talk in this ticket", userOpt("user", "Who to add", true)),
		sub("remove", "Take someone out of this ticket", userOpt("user", "Who to remove", true)),
		sub("rename", "Rename this ticket's channel", strOpt("name", "The new name", true, 90)),
		sub("list", "Tickets in this server (staff)", typeOption(false), status),
		sub("panel", "Post ticket types and forms as buttons on one message (staff)",
			strOpt("title", "The panel's heading", true, 256),
			strOpt("text", "Words under the heading (optional)", false, 2000),
			channelOpt("channel", "Where to post it (this channel if left empty)", false, panelTextChannels...)),
	}
}

// registerTickets adds /tickettype and /ticket, their autocomplete, buttons and pop-ups.
func registerTickets() {
	for _, c := range []*SlashCommandHandler{
		{Name: "tickettype", Description: "Set up ticket types: categories, staff, questions and buttons", Handler: handleTicketTypeCommand,
			Options: ticketTypeCommandOptions(), DefaultPermissions: &permManageGuild},
		// /ticket is visible to everyone: the member who opened a ticket may close it. Each subcommand checks who may use it.
		{Name: "ticket", Description: "Handle a ticket: close, claim, add or remove people, rename", Handler: handleTicketCommand,
			Options: ticketCommandOptions()},
	} {
		if _, exists := CommandManager.GetCommand(c.Name); exists {
			continue
		}
		c.Permissions = []string{"user"}
		c.GuildOnly = true
		if err := CommandManager.RegisterCommand(c.Name, c); err != nil {
			appLogger.Errorf("Failed to register %s command handler: %v", c.Name, err)
		}
	}
	registerAutocompleteRoute("tickettype", handleTicketTypeAutocomplete)
	registerAutocompleteRoute("ticket", handleTicketTypeAutocomplete)
	registerComponentRoute("ticket", handleOpenButton)
	registerComponentRoute("tickets", handleMyTicketsButton)
	registerComponentRoute("tkttry", handleTryButton)
	registerComponentRoute("tktnext", handleNextPageButton)
	registerComponentRoute("tkt", handleTicketControl)
	registerComponentRoute("tktdel", handleTypeDeleteButton)
	registerComponentRoute("tktpanel", handlePanelMaker)
	registerModalRoute("tktpage", handleIntakePageSubmit)
	registerModalRoute("tkttext", handleTypeTextSubmit)
	registerModalRoute("tktclose", handleCloseReasonSubmit)
}

func handleTicketTypeAutocomplete(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if tickets == nil || i.GuildID == "" || !canManageServer(i) {
		respondChoices(s, i, nil)
		return
	}
	typed := ""
	if f := focusedOption(i); f != nil {
		if f.Name != "type" {
			respondChoices(s, i, nil)
			return
		}
		typed = strings.ToLower(fmt.Sprint(f.Value))
	}
	types, err := tickets.ListTypes(i.GuildID)
	if err != nil {
		appLogger.Warnf("Ticket type autocomplete: %v", err)
		respondChoices(s, i, nil)
		return
	}
	choices := []*discordgo.ApplicationCommandOptionChoice{}
	for _, t := range types {
		name := fmt.Sprintf("%s (#%d)", t.Name, t.ID)
		if typed != "" && !strings.Contains(strings.ToLower(name), typed) {
			continue
		}
		choices = append(choices, &discordgo.ApplicationCommandOptionChoice{Name: truncateRunes(name, 100), Value: t.ID})
	}
	respondChoices(s, i, choices)
}

// ============================================================================
// SHARED CHECKS
// ============================================================================

func needType(guildID string, id int64) (*ticketType, error) {
	t, err := tickets.GetType(guildID, id)
	if err != nil {
		return nil, err
	}
	if t == nil {
		return nil, userErrorf("I can't find that ticket type. `/tickettype list` shows them.")
	}
	return t, nil
}

// isTicketStaff: Manage Server (or the staff role), or one of the type's staff roles.
func isTicketStaff(i *discordgo.InteractionCreate, tt *ticketType) (bool, error) {
	if canManageServer(i) {
		return true, nil
	}
	roles, err := tickets.StaffRoles(tt.ID)
	if err != nil {
		return false, err
	}
	for _, id := range roles {
		if hasRole(i.Member, id) {
			return true, nil
		}
	}
	return false, nil
}

func hasRole(member *discordgo.Member, roleID string) bool {
	if member == nil || roleID == "" {
		return false
	}
	for _, r := range member.Roles {
		if r == roleID {
			return true
		}
	}
	return false
}

// checkLogChannel: the bot must be able to post there, and @everyone must not see it unless allowPublic.
func checkLogChannel(s *discordgo.Session, guildID, channelID string, allowPublic bool) error {
	if err := checkPanelChannel(s, channelID); err != nil {
		return err
	}
	if !allowPublic && everyoneCanSee(s, guildID, channelID) {
		return userErrorf("Everyone can see <#%s>, and ticket transcripts hold members' private messages. Pick a staff-only channel, or add `allow_public:True` if you're sure.", channelID)
	}
	return nil
}

// everyoneCanSee reports whether @everyone has View Channel in a channel.
func everyoneCanSee(s *discordgo.Session, guildID, channelID string) bool {
	guild, err := lookupGuild(s, guildID)
	if err != nil {
		return false
	}
	ch, err := s.State.Channel(channelID)
	if err != nil {
		if ch, err = s.Channel(channelID); err != nil {
			return false
		}
	}
	var perms int64
	for _, r := range guild.Roles {
		if r.ID == guildID {
			perms = r.Permissions
		}
	}
	if perms&discordgo.PermissionAdministrator != 0 {
		return true
	}
	for _, o := range ch.PermissionOverwrites {
		if o.Type == discordgo.PermissionOverwriteTypeRole && o.ID == guildID {
			perms &^= o.Deny
			perms |= o.Allow
		}
	}
	return perms&discordgo.PermissionViewChannel != 0
}

// checkTicketCategory: the bot must be able to make and manage channels there.
func checkTicketCategory(s *discordgo.Session, guildID, categoryID string) error {
	facts, err := loadRoleFacts(s, guildID)
	if err != nil {
		return err
	}
	me := lookupMember(s, guildID, s.State.User.ID)
	if me == nil {
		return userErrorf("I couldn't check my own permissions. Please try again.")
	}
	guildPerms := guildPermissions(guildID, me.Roles, facts.Roles)
	need := int64(discordgo.PermissionManageChannels | discordgo.PermissionManageRoles)
	if guildPerms&discordgo.PermissionAdministrator == 0 && guildPerms&need != need {
		return userErrorf("I need the Manage Channels and Manage Roles permissions to make private ticket channels.")
	}
	perms, err := s.UserChannelPermissions(s.State.User.ID, categoryID)
	if err != nil {
		return err
	}
	if perms&discordgo.PermissionAdministrator == 0 && perms&(discordgo.PermissionViewChannel|discordgo.PermissionManageChannels) != discordgo.PermissionViewChannel|discordgo.PermissionManageChannels {
		return userErrorf("I need View Channel and Manage Channels in <#%s> to make tickets there.", categoryID)
	}
	if n := categoryChannelCount(s, guildID, categoryID); n >= categoryChannelLimit {
		return userErrorf("<#%s> is full (%d channels). Pick another category.", categoryID, categoryChannelLimit)
	}
	return nil
}

// checkStaffRole: a role that may see every ticket of a type.
func checkStaffRole(guildID string, role *discordgo.Role) error {
	if role == nil {
		return nil
	}
	if role.ID == guildID {
		return userErrorf("@everyone can't be ticket staff: every ticket would be public.")
	}
	return nil
}

// buttonEmojiText is an emoji as typed ("" to clear), or a mistake.
func buttonEmojiText(text string) (string, error) {
	e, err := parseButtonEmoji(text)
	if err != nil {
		return "", userErrorf("That isn't an emoji. Type one emoji, like 🎫, or `none` to have no emoji.")
	}
	if e == nil {
		return "", nil
	}
	return strings.TrimSpace(text), nil
}

func parseID(parts []string, at int) (int64, bool) {
	if at >= len(parts) {
		return 0, false
	}
	var id int64
	if _, err := fmt.Sscanf(parts[at], "%d", &id); err != nil || id <= 0 || fmt.Sprint(id) != parts[at] {
		return 0, false
	}
	return id, true
}

// updateMessage replaces the message a button was on (used for ephemeral menus).
func updateMessage(s *discordgo.Session, i *discordgo.InteractionCreate, content string, components []discordgo.MessageComponent) {
	if components == nil {
		components = []discordgo.MessageComponent{}
	}
	err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseUpdateMessage,
		Data: &discordgo.InteractionResponseData{Content: content, Components: components, AllowedMentions: &discordgo.MessageAllowedMentions{}},
	})
	if err != nil {
		appLogger.Warnf("Could not update a message: %v", err)
	}
}

func showModal(s *discordgo.Session, i *discordgo.InteractionCreate, data *discordgo.InteractionResponseData) error {
	return s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{Type: discordgo.InteractionResponseModal, Data: data})
}

func interactionUser(i *discordgo.InteractionCreate) *discordgo.User {
	if i.Member != nil && i.Member.User != nil {
		return i.Member.User
	}
	return i.User
}

func lookupMember(s *discordgo.Session, guildID, userID string) *discordgo.Member {
	if m, err := s.State.Member(guildID, userID); err == nil {
		return m
	}
	if m, err := s.GuildMember(guildID, userID); err == nil {
		return m
	}
	return nil
}

func ticketsOff(s *discordgo.Session, i *discordgo.InteractionCreate) bool {
	if tickets == nil {
		respondEphemeral(s, i, "Tickets need the bot's database, which isn't available right now.")
		return true
	}
	if i.GuildID == "" || i.Member == nil {
		respondEphemeral(s, i, "Tickets only work inside a server.")
		return true
	}
	return false
}
