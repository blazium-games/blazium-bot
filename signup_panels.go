package main

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/bwmarrin/discordgo"
)

// Sign-up panels: a message with buttons that give (or take away) the roles you choose.
//
//	/panel create, add-role, remove-role, edit, move, refresh, list, delete   (Manage Server)
//
// Each button's custom ID is "signup:<panel id>:<role id>". Discord sends it back on
// every click, so the bot needs nothing in memory and panels survive restarts.
// DO NOT change that format: buttons on panels already posted would stop working.

const (
	signupModeToggle  = "toggle"
	signupModeAddOnly = "add_only"
	signupModeSingle  = "single"

	signupMaxRoles = 25 // Discord allows 5 rows of 5 buttons on one message
	signupColor    = 0x5865F2
)

var signupModeHelp = map[string]string{
	signupModeToggle:  "Click a button to get the role. Click it again to remove it.",
	signupModeAddOnly: "Click a button to get the role.",
	signupModeSingle:  "Pick one. Choosing another swaps your role; clicking yours again removes it.",
}

var signupStyles = map[string]discordgo.ButtonStyle{
	"grey":  discordgo.SecondaryButton,
	"blue":  discordgo.PrimaryButton,
	"green": discordgo.SuccessButton,
	"red":   discordgo.DangerButton,
}

// riskyPermissions: a role carrying any of these is never handed out by a button anyone can click.
var riskyPermissions = []struct {
	bit  int64
	name string
}{
	{discordgo.PermissionAdministrator, "Administrator"},
	{discordgo.PermissionManageGuild, "Manage Server"},
	{discordgo.PermissionManageRoles, "Manage Roles"},
	{discordgo.PermissionManageChannels, "Manage Channels"},
	{discordgo.PermissionManageMessages, "Manage Messages"},
	{discordgo.PermissionManageWebhooks, "Manage Webhooks"},
	{discordgo.PermissionManageGuildExpressions, "Manage Expressions"},
	{discordgo.PermissionManageThreads, "Manage Threads"},
	{discordgo.PermissionKickMembers, "Kick Members"},
	{discordgo.PermissionBanMembers, "Ban Members"},
	{discordgo.PermissionModerateMembers, "Timeout Members"},
	{discordgo.PermissionMentionEveryone, "Mention Everyone"},
}

func initSignupPanels() {
	db := botDatabase()
	if db == nil {
		appLogger.Warn("Postgres is unavailable; sign-up panels disabled")
		return
	}
	store, err := newPostgresSignupStore(db)
	if err != nil {
		appLogger.Errorf("Sign-up panels disabled: %v", err)
		return
	}
	signupPanels = store
	appLogger.Info("Sign-up panels ready")
}

// ============================================================================
// ROLE RULES (no Discord calls, so they can be tested)
// ============================================================================

// guildRoleFacts is what the role rules need to know about a server and the bot in it.
type guildRoleFacts struct {
	GuildID        string
	OwnerID        string
	Roles          map[string]*discordgo.Role
	BotTop         int  // position of the bot's highest role
	BotManageRoles bool // the bot has Manage Roles (or Administrator)
}

// roleProblem is why the bot must not (or cannot) hand out role; "" if it's fine.
func roleProblem(role *discordgo.Role, f *guildRoleFacts) string {
	if role.ID == f.GuildID {
		return "@everyone can't be given out"
	}
	if role.Managed {
		return "it is managed by an integration or by Discord (bot role, booster role, etc.)"
	}
	var risky []string
	for _, p := range riskyPermissions {
		if role.Permissions&p.bit != 0 {
			risky = append(risky, p.name)
		}
	}
	if len(risky) > 0 {
		if len(risky) > 4 {
			risky = risky[:4]
		}
		return "it carries staff-level permissions (" + strings.Join(risky, ", ") + ")"
	}
	if !f.BotManageRoles {
		return "I don't have the Manage Roles permission"
	}
	if role.Position >= f.BotTop {
		return "it sits above (or level with) my highest role, so Discord won't let me assign it"
	}
	return ""
}

// topPosition is the position of the highest of roleIDs (0 if none are known).
func topPosition(roleIDs []string, roles map[string]*discordgo.Role) int {
	top := 0
	for _, id := range roleIDs {
		if r, ok := roles[id]; ok && r.Position > top {
			top = r.Position
		}
	}
	return top
}

// guildPermissions is a member's server-wide permissions from @everyone and their roles.
func guildPermissions(guildID string, roleIDs []string, roles map[string]*discordgo.Role) int64 {
	var perms int64
	if everyone, ok := roles[guildID]; ok {
		perms |= everyone.Permissions
	}
	for _, id := range roleIDs {
		if r, ok := roles[id]; ok {
			perms |= r.Permissions
		}
	}
	return perms
}

// signupClick is what one button press does.
type signupClick struct {
	Add     string   // role to give, or ""
	Remove  []string // roles to take away
	Message string
}

func roleMention(id string) string { return "<@&" + id + ">" }

func mentionAll(ids []string) string {
	parts := make([]string, len(ids))
	for n, id := range ids {
		parts[n] = roleMention(id)
	}
	return strings.Join(parts, ", ")
}

// planSignupClick decides what a press of roleID's button does for a member holding
// memberRoles. panelRoles are all the panel's role IDs. problem says why a role can't be
// handed out right now ("" if it can).
func planSignupClick(mode string, memberRoles []string, roleID string, panelRoles []string, problem func(string) string) (signupClick, error) {
	has := map[string]bool{}
	for _, id := range memberRoles {
		has[id] = true
	}
	role := roleMention(roleID)
	if mode == signupModeAddOnly {
		if has[roleID] {
			return signupClick{Message: "You already have " + role + "."}, nil
		}
		return signupClick{Add: roleID, Message: "✅ You now have " + role + "."}, nil
	}
	if has[roleID] {
		return signupClick{Remove: []string{roleID}, Message: "Removed " + role + "."}, nil
	}
	click := signupClick{Add: roleID, Message: "✅ You now have " + role + "."}
	if mode == signupModeSingle {
		for _, other := range panelRoles {
			if other == roleID || !has[other] {
				continue
			}
			// Only swap away roles the panel could hand out right now: one that has since
			// become a staff role (or moved above the bot) stays.
			if why := problem(other); why != "" {
				return signupClick{}, userErrorf("I can't swap you from %s to %s right now: for %s, %s. Please let an admin know.",
					roleMention(other), role, roleMention(other), why)
			}
			click.Remove = append(click.Remove, other)
		}
		if len(click.Remove) > 0 {
			click.Message = fmt.Sprintf("✅ You now have %s (replaced %s).", role, mentionAll(click.Remove))
		}
	}
	return click, nil
}

// ============================================================================
// RENDERING
// ============================================================================

var customEmojiRe = regexp.MustCompile(`^<(a?):([A-Za-z0-9_~]{1,32}):(\d{15,25})>$`)

// parseButtonEmoji turns what staff typed ("🎮", "<:blazium:123…>") into a button emoji.
func parseButtonEmoji(text string) (*discordgo.ComponentEmoji, error) {
	text = strings.TrimSpace(text)
	if text == "" || strings.EqualFold(text, "none") {
		return nil, nil
	}
	if m := customEmojiRe.FindStringSubmatch(text); m != nil {
		return &discordgo.ComponentEmoji{Name: m[2], ID: m[3], Animated: m[1] == "a"}, nil
	}
	if len([]rune(text)) > 8 {
		return nil, userErrorf("That isn't an emoji. Use one standard emoji, or one from this server.")
	}
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return nil, userErrorf("That isn't an emoji. Use one standard emoji, or one from this server.")
		}
	}
	return &discordgo.ComponentEmoji{Name: text}, nil
}

func signupButtonID(panelID int64, roleID string) string {
	return fmt.Sprintf("signup:%d:%s", panelID, roleID)
}

// parseSignupButtonID reads "signup:<panel id>:<role id>".
func parseSignupButtonID(customID string) (int64, string, bool) {
	parts := strings.Split(customID, ":")
	if len(parts) != 3 || parts[0] != "signup" {
		return 0, "", false
	}
	panelID, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || panelID <= 0 {
		return 0, "", false
	}
	if _, err := strconv.ParseUint(parts[2], 10, 64); err != nil {
		return 0, "", false
	}
	return panelID, parts[2], true
}

// buildSignupPanel is the panel's embed and buttons.
func buildSignupPanel(p *signupPanel, roles []signupPanelRole) (*discordgo.MessageEmbed, []discordgo.MessageComponent) {
	var parts []string
	if p.Description != "" {
		parts = append(parts, p.Description)
	}
	if len(roles) > 0 {
		lines := make([]string, len(roles))
		for n, r := range roles {
			prefix := ""
			if r.Emoji != "" {
				prefix = r.Emoji + " "
			}
			lines[n] = fmt.Sprintf("%s**%s** → %s", prefix, r.Label, roleMention(r.RoleID))
		}
		parts = append(parts, strings.Join(lines, "\n"))
	} else {
		parts = append(parts, "*No sign-up options yet.*")
	}
	desc := strings.Join(parts, "\n\n")
	if len(desc) > 4096 {
		desc = desc[:4096]
	}
	help, ok := signupModeHelp[p.Mode]
	if !ok {
		help = signupModeHelp[signupModeToggle]
	}
	embed := &discordgo.MessageEmbed{
		Title:       p.Title,
		Description: desc,
		Color:       signupColor,
		Footer:      &discordgo.MessageEmbedFooter{Text: help},
	}
	rows := []discordgo.MessageComponent{}
	var row discordgo.ActionsRow
	for _, r := range roles {
		button := discordgo.Button{
			Label:    r.Label,
			Style:    discordgo.ButtonStyle(r.Style),
			CustomID: signupButtonID(p.ID, r.RoleID),
		}
		if emoji, err := parseButtonEmoji(r.Emoji); err == nil && emoji != nil {
			button.Emoji = emoji
		}
		row.Components = append(row.Components, button)
		if len(row.Components) == 5 {
			rows = append(rows, row)
			row = discordgo.ActionsRow{}
		}
	}
	if len(row.Components) > 0 {
		rows = append(rows, row)
	}
	return embed, rows
}

// renderSignupPanel posts the panel, or updates the message already posted. A deleted
// message is posted again and the new message saved.
func renderSignupPanel(s *discordgo.Session, p *signupPanel) (*discordgo.Message, error) {
	roles, err := signupPanels.PanelRoles(p.ID)
	if err != nil {
		return nil, err
	}
	embed, components := buildSignupPanel(p, roles)
	embeds := []*discordgo.MessageEmbed{embed}
	if p.MessageID != "" {
		msg, err := s.ChannelMessageEditComplex(&discordgo.MessageEdit{
			ID: p.MessageID, Channel: p.ChannelID, Embeds: &embeds, Components: &components,
		})
		if err == nil {
			return msg, nil
		}
		if !isNotFound(err) {
			return nil, err
		}
		// Someone deleted the message (or the channel): post a fresh one below.
	}
	msg, err := s.ChannelMessageSendComplex(p.ChannelID, &discordgo.MessageSend{
		Embeds: embeds, Components: components, AllowedMentions: &discordgo.MessageAllowedMentions{},
	})
	if err != nil {
		if isNotFound(err) {
			return nil, userErrorf("Panel #%d's channel is gone. Use `/panel move` to put it somewhere else.", p.ID)
		}
		return nil, err
	}
	p.MessageID = msg.ID
	if err := signupPanels.UpdatePanel(p); err != nil {
		return nil, err
	}
	return msg, nil
}

func messageLink(guildID, channelID, messageID string) string {
	return fmt.Sprintf("https://discord.com/channels/%s/%s/%s", guildID, channelID, messageID)
}

// ============================================================================
// DISCORD LOOKUPS
// ============================================================================

func lookupGuild(s *discordgo.Session, guildID string) (*discordgo.Guild, error) {
	if g, err := s.State.Guild(guildID); err == nil && len(g.Roles) > 0 {
		return g, nil
	}
	return s.Guild(guildID)
}

// loadRoleFacts gathers the server's roles and the bot's standing in it.
func loadRoleFacts(s *discordgo.Session, guildID string) (*guildRoleFacts, error) {
	guild, err := lookupGuild(s, guildID)
	if err != nil {
		return nil, err
	}
	roles := make(map[string]*discordgo.Role, len(guild.Roles))
	for _, r := range guild.Roles {
		roles[r.ID] = r
	}
	if len(roles) == 0 {
		fetched, err := s.GuildRoles(guildID)
		if err != nil {
			return nil, err
		}
		for _, r := range fetched {
			roles[r.ID] = r
		}
	}
	botID := s.State.User.ID
	me, err := s.State.Member(guildID, botID)
	if err != nil {
		if me, err = s.GuildMember(guildID, botID); err != nil {
			return nil, err
		}
	}
	perms := guildPermissions(guildID, me.Roles, roles)
	return &guildRoleFacts{
		GuildID:        guildID,
		OwnerID:        guild.OwnerID,
		Roles:          roles,
		BotTop:         topPosition(me.Roles, roles),
		BotManageRoles: perms&(discordgo.PermissionManageRoles|discordgo.PermissionAdministrator) != 0,
	}, nil
}

// checkPanelChannel makes sure the bot can post a panel in channelID.
func checkPanelChannel(s *discordgo.Session, channelID string) error {
	ch, err := s.State.Channel(channelID)
	if err != nil {
		if ch, err = s.Channel(channelID); err != nil {
			return userErrorf("I can't see that channel.")
		}
	}
	if ch.Type != discordgo.ChannelTypeGuildText && ch.Type != discordgo.ChannelTypeGuildNews {
		return userErrorf("Panels can only be posted in a regular text channel. Pick one with the `channel` option.")
	}
	perms, err := s.UserChannelPermissions(s.State.User.ID, channelID)
	if err != nil {
		return err
	}
	need := int64(discordgo.PermissionViewChannel | discordgo.PermissionSendMessages | discordgo.PermissionEmbedLinks)
	if perms&discordgo.PermissionAdministrator == 0 && perms&need != need {
		return userErrorf("I need View Channel, Send Messages and Embed Links in <#%s> to post a panel there.", channelID)
	}
	return nil
}

// ============================================================================
// BUTTON CLICKS
// ============================================================================

func handleSignupButton(s *discordgo.Session, i *discordgo.InteractionCreate, customID string) {
	panelID, roleID, ok := parseSignupButtonID(customID)
	if !ok {
		respondEphemeral(s, i, "This button no longer does anything.")
		return
	}
	if i.GuildID == "" || i.Member == nil || i.Member.User == nil {
		respondEphemeral(s, i, "Sign-up buttons only work inside a server.")
		return
	}
	if signupPanels == nil {
		respondEphemeral(s, i, "Sign-ups are switched off right now. Please try again later.")
		return
	}
	if err := deferEphemeral(s, i); err != nil {
		appLogger.Warnf("Could not defer a sign-up click: %v", err)
		return
	}
	msg, err := applySignupClick(s, i, panelID, roleID)
	if err != nil {
		var rest *discordgo.RESTError
		if errors.As(err, &rest) && rest.Response != nil && rest.Response.StatusCode == 403 {
			msg = "Discord wouldn't let me change your roles. Please let an admin know."
		} else {
			msg = errorText(fmt.Sprintf("Sign-up click (panel %d, role %s)", panelID, roleID), err)
		}
	}
	editResponse(s, i, msg)
}

func applySignupClick(s *discordgo.Session, i *discordgo.InteractionCreate, panelID int64, roleID string) (string, error) {
	panel, err := signupPanels.GetPanel(i.GuildID, panelID)
	if err != nil {
		return "", err
	}
	roles, err := signupPanels.PanelRoles(panelID)
	if err != nil {
		return "", err
	}
	panelRoleIDs := make([]string, 0, len(roles))
	onPanel := false
	for _, r := range roles {
		panelRoleIDs = append(panelRoleIDs, r.RoleID)
		onPanel = onPanel || r.RoleID == roleID
	}
	if panel == nil || !onPanel {
		return "", userErrorf("This sign-up option no longer exists.")
	}
	facts, err := loadRoleFacts(s, i.GuildID)
	if err != nil {
		return "", err
	}
	role, ok := facts.Roles[roleID]
	if !ok {
		return "", userErrorf("That role has been deleted. Please let an admin know.")
	}
	// Checked on every click: the role may have gained permissions or moved above the bot.
	if why := roleProblem(role, facts); why != "" {
		return "", userErrorf("I can't give out %s right now: %s. Please let an admin know.", roleMention(roleID), why)
	}
	problem := func(id string) string {
		r, ok := facts.Roles[id]
		if !ok {
			return "" // deleted roles can't be held; nothing to swap
		}
		return roleProblem(r, facts)
	}
	click, err := planSignupClick(panel.Mode, i.Member.Roles, roleID, panelRoleIDs, problem)
	if err != nil {
		return "", err
	}
	reason := discordgo.WithAuditLogReason(fmt.Sprintf("Sign-up panel #%d", panelID))
	userID := i.Member.User.ID
	for _, id := range click.Remove {
		if err := s.GuildMemberRoleRemove(i.GuildID, userID, id, reason); err != nil {
			return "", err
		}
	}
	if click.Add != "" {
		if err := s.GuildMemberRoleAdd(i.GuildID, userID, click.Add, reason); err != nil {
			return "", err
		}
	}
	return click.Message, nil
}

// handleSignupRoleDelete: a deleted role's buttons would be dead, so remove them and redraw.
func handleSignupRoleDelete(s *discordgo.Session, e *discordgo.GuildRoleDelete) {
	if signupPanels == nil || e == nil {
		return
	}
	touched, err := signupPanels.RemoveRoleEverywhere(e.GuildID, e.RoleID)
	if err != nil {
		appLogger.Errorf("Could not remove deleted role %s from panels: %v", e.RoleID, err)
		return
	}
	for _, id := range touched {
		panel, err := signupPanels.GetPanel(e.GuildID, id)
		if err != nil || panel == nil {
			continue
		}
		if _, err := renderSignupPanel(s, panel); err != nil {
			appLogger.Warnf("Could not redraw panel %d after role deletion: %v", id, err)
		}
	}
}

// ============================================================================
// COMMANDS
// ============================================================================

func signupPanelOption(description string) *discordgo.ApplicationCommandOption {
	return &discordgo.ApplicationCommandOption{
		Type: discordgo.ApplicationCommandOptionInteger, Name: "panel", Description: description,
		Required: true, Autocomplete: true, MinValue: floatPtr(1),
	}
}

func floatPtr(v float64) *float64 { return &v }

var panelTextChannels = []discordgo.ChannelType{discordgo.ChannelTypeGuildText, discordgo.ChannelTypeGuildNews}

func signupModeChoices() []*discordgo.ApplicationCommandOptionChoice {
	return []*discordgo.ApplicationCommandOptionChoice{
		{Name: "toggle: click on, click off", Value: signupModeToggle},
		{Name: "add_only: clicking never removes the role", Value: signupModeAddOnly},
		{Name: "single: one role per member", Value: signupModeSingle},
	}
}

func panelCommandOptions() []*discordgo.ApplicationCommandOption {
	return []*discordgo.ApplicationCommandOption{
		{
			Type: discordgo.ApplicationCommandOptionSubCommand, Name: "create",
			Description: "Post a new sign-up panel. Add its buttons with /panel add-role.",
			Options: []*discordgo.ApplicationCommandOption{
				{Type: discordgo.ApplicationCommandOptionString, Name: "title", Description: "Heading shown at the top of the panel", Required: true, MaxLength: 200},
				{Type: discordgo.ApplicationCommandOptionString, Name: "description", Description: `Text under the heading. Type \n for a line break.`, MaxLength: 2000},
				{Type: discordgo.ApplicationCommandOptionChannel, Name: "channel", Description: "Where to post it (default: this channel)", ChannelTypes: panelTextChannels},
				{Type: discordgo.ApplicationCommandOptionString, Name: "mode", Description: "How the buttons behave (default: toggle)", Choices: signupModeChoices()},
			},
		},
		{
			Type: discordgo.ApplicationCommandOptionSubCommand, Name: "add-role",
			Description: "Add a button to a panel that gives a role.",
			Options: []*discordgo.ApplicationCommandOption{
				signupPanelOption("Which panel"),
				{Type: discordgo.ApplicationCommandOptionRole, Name: "role", Description: "Role the button gives", Required: true},
				{Type: discordgo.ApplicationCommandOptionString, Name: "label", Description: "Text on the button (default: the role's name)", MaxLength: 80},
				{Type: discordgo.ApplicationCommandOptionString, Name: "emoji", Description: "Optional emoji shown on the button", MaxLength: 60},
				{Type: discordgo.ApplicationCommandOptionString, Name: "color", Description: "Button color (default: grey)", Choices: []*discordgo.ApplicationCommandOptionChoice{
					{Name: "grey", Value: "grey"}, {Name: "blue", Value: "blue"}, {Name: "green", Value: "green"}, {Name: "red", Value: "red"},
				}},
			},
		},
		{
			Type: discordgo.ApplicationCommandOptionSubCommand, Name: "remove-role",
			Description: "Remove a role's button from a panel.",
			Options: []*discordgo.ApplicationCommandOption{
				signupPanelOption("Which panel"),
				{Type: discordgo.ApplicationCommandOptionRole, Name: "role", Description: "Role whose button to remove", Required: true},
			},
		},
		{
			Type: discordgo.ApplicationCommandOptionSubCommand, Name: "edit",
			Description: "Change a panel's title, text or mode.",
			Options: []*discordgo.ApplicationCommandOption{
				signupPanelOption("Which panel"),
				{Type: discordgo.ApplicationCommandOptionString, Name: "title", Description: "New heading", MaxLength: 200},
				{Type: discordgo.ApplicationCommandOptionString, Name: "description", Description: `New text. Type \n for a line break.`, MaxLength: 2000},
				{Type: discordgo.ApplicationCommandOptionString, Name: "mode", Description: "How the buttons behave", Choices: signupModeChoices()},
			},
		},
		{
			Type: discordgo.ApplicationCommandOptionSubCommand, Name: "move",
			Description: "Move a panel to another channel.",
			Options: []*discordgo.ApplicationCommandOption{
				signupPanelOption("Which panel"),
				{Type: discordgo.ApplicationCommandOptionChannel, Name: "channel", Description: "Channel to move it to", Required: true, ChannelTypes: panelTextChannels},
			},
		},
		{
			Type: discordgo.ApplicationCommandOptionSubCommand, Name: "refresh",
			Description: "Redraw a panel, or post it again if its message was deleted.",
			Options:     []*discordgo.ApplicationCommandOption{signupPanelOption("Which panel")},
		},
		{
			Type: discordgo.ApplicationCommandOptionSubCommand, Name: "list",
			Description: "List this server's sign-up panels.",
		},
		{
			Type: discordgo.ApplicationCommandOptionSubCommand, Name: "delete",
			Description: "Delete a panel and its message. Members keep roles they already got.",
			Options:     []*discordgo.ApplicationCommandOption{signupPanelOption("Which panel")},
		},
	}
}

// registerSignupPanels adds /panel, its autocomplete and its buttons.
func registerSignupPanels() {
	if _, exists := CommandManager.GetCommand("panel"); !exists {
		err := CommandManager.RegisterCommand("panel", &SlashCommandHandler{
			Name:               "panel",
			Description:        "Sign-up panels that give members roles",
			Handler:            handlePanelCommand,
			Permissions:        []string{"user"},
			GuildOnly:          true,
			DefaultPermissions: &permManageGuild,
			Options:            panelCommandOptions(),
		})
		if err != nil {
			appLogger.Errorf("Failed to register panel command handler: %v", err)
		}
	}
	registerAutocompleteRoute("panel", handlePanelAutocomplete)
	registerComponentRoute("signup", handleSignupButton)
}

func handlePanelAutocomplete(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if signupPanels == nil || i.GuildID == "" || !canManageServer(i) {
		respondChoices(s, i, nil)
		return
	}
	typed := ""
	if f := focusedOption(i); f != nil {
		typed = strings.ToLower(fmt.Sprint(f.Value))
	}
	panels, err := signupPanels.ListPanels(i.GuildID)
	if err != nil {
		appLogger.Warnf("Panel autocomplete: %v", err)
		respondChoices(s, i, nil)
		return
	}
	choices := []*discordgo.ApplicationCommandOptionChoice{}
	for n := len(panels) - 1; n >= 0; n-- { // newest first
		p := panels[n]
		name := fmt.Sprintf("#%d · %s", p.ID, p.Title)
		if typed != "" && !strings.Contains(strings.ToLower(name), typed) {
			continue
		}
		if len(name) > 100 {
			name = name[:100]
		}
		choices = append(choices, &discordgo.ApplicationCommandOptionChoice{Name: name, Value: p.ID})
	}
	respondChoices(s, i, choices)
}

func handlePanelCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if i.GuildID == "" || i.Member == nil {
		respondEphemeral(s, i, "Panels only work inside a server.")
		return
	}
	if !canManageServer(i) {
		respondEphemeral(s, i, "You need Manage Server (or the staff role) to manage sign-up panels.")
		return
	}
	if signupPanels == nil {
		respondEphemeral(s, i, "Sign-up panels need the bot's database, which isn't available right now.")
		return
	}
	if err := deferEphemeral(s, i); err != nil {
		appLogger.Warnf("Could not defer /panel: %v", err)
		return
	}
	sub, opts := commandOptions(i)
	msg, err := runPanelSubcommand(s, i, sub, opts)
	if err != nil {
		msg = errorText("/panel "+sub, err)
	}
	editResponse(s, i, msg)
}

func runPanelSubcommand(s *discordgo.Session, i *discordgo.InteractionCreate, sub string, opts map[string]*discordgo.ApplicationCommandInteractionDataOption) (string, error) {
	if sub == "create" {
		return panelCreate(s, i, opts)
	}
	if sub == "list" {
		return panelList(i)
	}
	id, _ := optInt(opts, "panel")
	panel, err := signupPanels.GetPanel(i.GuildID, id)
	if err != nil {
		return "", err
	}
	if panel == nil {
		return "", userErrorf("There is no panel #%d in this server. See `/panel list`.", id)
	}
	switch sub {
	case "add-role":
		return panelAddRole(s, i, opts, panel)
	case "remove-role":
		return panelRemoveRole(s, i, opts, panel)
	case "edit":
		return panelEdit(s, opts, panel)
	case "move":
		return panelMove(s, i, opts, panel)
	case "refresh":
		msg, err := renderSignupPanel(s, panel)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("Panel #%d is up to date: %s", panel.ID, messageLink(i.GuildID, msg.ChannelID, msg.ID)), nil
	case "delete":
		deletePanelMessage(s, panel)
		if err := signupPanels.DeletePanel(panel.ID); err != nil {
			return "", err
		}
		return fmt.Sprintf("Panel #%d deleted. Members keep the roles they already got.", panel.ID), nil
	}
	return "", userErrorf("Unknown subcommand.")
}

func panelText(raw string) string {
	return strings.ReplaceAll(raw, `\n`, "\n")
}

func panelCreate(s *discordgo.Session, i *discordgo.InteractionCreate, opts map[string]*discordgo.ApplicationCommandInteractionDataOption) (string, error) {
	title, _ := optString(opts, "title")
	title = strings.TrimSpace(title)
	if title == "" {
		return "", userErrorf("The panel needs a title.")
	}
	description, _ := optString(opts, "description")
	mode, ok := optString(opts, "mode")
	if !ok {
		mode = signupModeToggle
	}
	channelID := i.ChannelID
	if ch := optChannel(i, opts, "channel"); ch != nil {
		channelID = ch.ID
	}
	if err := checkPanelChannel(s, channelID); err != nil {
		return "", err
	}
	panel := &signupPanel{
		GuildID: i.GuildID, ChannelID: channelID, Title: title,
		Description: panelText(description), Mode: mode, CreatedBy: i.Member.User.ID,
	}
	id, err := signupPanels.CreatePanel(panel)
	if err != nil {
		return "", err
	}
	panel.ID = id
	msg, err := renderSignupPanel(s, panel)
	if err != nil {
		_ = signupPanels.DeletePanel(id)
		return "", err
	}
	return fmt.Sprintf("Panel **#%d** posted: %s\nNow add buttons with `/panel add-role panel:%d role:@YourRole`.",
		id, messageLink(i.GuildID, msg.ChannelID, msg.ID), id), nil
}

func panelAddRole(s *discordgo.Session, i *discordgo.InteractionCreate, opts map[string]*discordgo.ApplicationCommandInteractionDataOption, panel *signupPanel) (string, error) {
	facts, err := loadRoleFacts(s, i.GuildID)
	if err != nil {
		return "", err
	}
	picked := optRole(i, opts, "role")
	role, ok := facts.Roles[picked.ID]
	if !ok {
		return "", userErrorf("I can't find that role.")
	}
	if why := roleProblem(role, facts); why != "" {
		return "", userErrorf("I can't put %s on a panel: %s.", roleMention(role.ID), why)
	}
	if i.Member.User.ID != facts.OwnerID && role.Position >= topPosition(i.Member.Roles, facts.Roles) {
		return "", userErrorf("%s is not below your own highest role, so you can't offer it.", roleMention(role.ID))
	}
	existing, err := signupPanels.PanelRoles(panel.ID)
	if err != nil {
		return "", err
	}
	for _, r := range existing {
		if r.RoleID == role.ID {
			return "", userErrorf("%s is already on panel #%d.", roleMention(role.ID), panel.ID)
		}
	}
	if len(existing) >= signupMaxRoles {
		return "", userErrorf("A panel holds at most %d buttons. Create another panel for more.", signupMaxRoles)
	}
	label, _ := optString(opts, "label")
	label = strings.TrimSpace(label)
	if label == "" {
		label = role.Name
	}
	if len([]rune(label)) > 80 {
		label = string([]rune(label)[:80])
	}
	emojiText, _ := optString(opts, "emoji")
	emoji, err := parseButtonEmoji(emojiText)
	if err != nil {
		return "", err
	}
	stored := ""
	if emoji != nil {
		stored = strings.TrimSpace(emojiText)
	}
	color, ok := optString(opts, "color")
	style, known := signupStyles[color]
	if !ok || !known {
		style = discordgo.SecondaryButton
	}
	entry := &signupPanelRole{PanelID: panel.ID, RoleID: role.ID, Label: label, Emoji: stored, Style: int(style), Position: len(existing)}
	if err := signupPanels.AddPanelRole(entry); err != nil {
		if errors.Is(err, errAlreadyExists) {
			return "", userErrorf("%s is already on panel #%d.", roleMention(role.ID), panel.ID)
		}
		return "", err
	}
	if _, err := renderSignupPanel(s, panel); err != nil {
		// Most often an emoji Discord doesn't recognise. Undo and explain.
		_ = signupPanels.RemovePanelRole(panel.ID, role.ID)
		var rest *discordgo.RESTError
		if errors.As(err, &rest) && rest.Response != nil && rest.Response.StatusCode == 400 {
			return "", userErrorf("Discord rejected that button, so nothing was changed. This is usually the emoji: use a standard emoji or one from this server.")
		}
		return "", err
	}
	return fmt.Sprintf("Added %s to panel #%d.", roleMention(role.ID), panel.ID), nil
}

func panelRemoveRole(s *discordgo.Session, i *discordgo.InteractionCreate, opts map[string]*discordgo.ApplicationCommandInteractionDataOption, panel *signupPanel) (string, error) {
	role := optRole(i, opts, "role")
	existing, err := signupPanels.PanelRoles(panel.ID)
	if err != nil {
		return "", err
	}
	found := false
	for _, r := range existing {
		found = found || r.RoleID == role.ID
	}
	if !found {
		return "", userErrorf("%s isn't on panel #%d.", roleMention(role.ID), panel.ID)
	}
	if err := signupPanels.RemovePanelRole(panel.ID, role.ID); err != nil {
		return "", err
	}
	if _, err := renderSignupPanel(s, panel); err != nil {
		return "", err
	}
	return fmt.Sprintf("Removed %s from panel #%d. Members who already have the role keep it.", roleMention(role.ID), panel.ID), nil
}

func panelEdit(s *discordgo.Session, opts map[string]*discordgo.ApplicationCommandInteractionDataOption, panel *signupPanel) (string, error) {
	title, hasTitle := optString(opts, "title")
	description, hasDesc := optString(opts, "description")
	mode, hasMode := optString(opts, "mode")
	if !hasTitle && !hasDesc && !hasMode {
		return "", userErrorf("Nothing to change. Fill in at least one of title, description or mode.")
	}
	if hasTitle && strings.TrimSpace(title) != "" {
		panel.Title = strings.TrimSpace(title)
	}
	if hasDesc {
		panel.Description = panelText(description)
	}
	if hasMode {
		panel.Mode = mode
	}
	if err := signupPanels.UpdatePanel(panel); err != nil {
		return "", err
	}
	if _, err := renderSignupPanel(s, panel); err != nil {
		return "", err
	}
	return fmt.Sprintf("Panel #%d updated.", panel.ID), nil
}

func panelMove(s *discordgo.Session, i *discordgo.InteractionCreate, opts map[string]*discordgo.ApplicationCommandInteractionDataOption, panel *signupPanel) (string, error) {
	ch := optChannel(i, opts, "channel")
	if ch == nil {
		return "", userErrorf("Pick a channel to move the panel to.")
	}
	if err := checkPanelChannel(s, ch.ID); err != nil {
		return "", err
	}
	deletePanelMessage(s, panel)
	panel.ChannelID, panel.MessageID = ch.ID, ""
	if err := signupPanels.UpdatePanel(panel); err != nil {
		return "", err
	}
	msg, err := renderSignupPanel(s, panel)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Panel #%d moved: %s", panel.ID, messageLink(i.GuildID, msg.ChannelID, msg.ID)), nil
}

func panelList(i *discordgo.InteractionCreate) (string, error) {
	panels, err := signupPanels.ListPanels(i.GuildID)
	if err != nil {
		return "", err
	}
	if len(panels) == 0 {
		return "", userErrorf("No panels yet. Make one with `/panel create`.")
	}
	var b strings.Builder
	for _, p := range panels {
		line := fmt.Sprintf("**#%d** %s · <#%s> · %d role(s) · %s\n", p.ID, p.Title, p.ChannelID, p.RoleCount, p.Mode)
		if b.Len()+len(line) > 1900 {
			b.WriteString("…and more.")
			break
		}
		b.WriteString(line)
	}
	return strings.TrimSpace(b.String()), nil
}

// deletePanelMessage removes a panel's posted message; it may already be gone.
func deletePanelMessage(s *discordgo.Session, panel *signupPanel) {
	if panel.MessageID == "" {
		return
	}
	if err := s.ChannelMessageDelete(panel.ChannelID, panel.MessageID); err != nil && !isNotFound(err) {
		appLogger.Warnf("Could not delete panel %d's message: %v", panel.ID, err)
	}
}
