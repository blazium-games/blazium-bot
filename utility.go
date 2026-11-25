package main

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
)

// Everyday information commands anyone can use, and /help built from the commands the
// bot actually has, so it never goes out of date.
//
//	/serverinfo  /userinfo [member]  /avatar [user]  /roleinfo role  /help

func utilityCommands() []*SlashCommandHandler {
	return []*SlashCommandHandler{
		{Name: "serverinfo", Description: "About this server: members, channels, roles, boosts", Handler: handleServerInfoCommand},
		{Name: "userinfo", Description: "About a member: when they joined, their roles, their account", Handler: handleUserInfoCommand,
			Options: []*discordgo.ApplicationCommandOption{userOpt("member", "Who (you if empty)", false)}},
		{Name: "avatar", Description: "Show someone's avatar in full size", Handler: handleAvatarCommand,
			Options: []*discordgo.ApplicationCommandOption{userOpt("user", "Whose (yours if empty)", false)}},
		{Name: "roleinfo", Description: "About a role: color, position, permissions", Handler: handleRoleInfoCommand,
			Options: []*discordgo.ApplicationCommandOption{roleOpt("role", "Which role", true)}},
	}
}

func registerUtility() {
	for _, c := range utilityCommands() {
		registerGuildCommand(c)
	}
}

func respondEmbed(s *discordgo.Session, i *discordgo.InteractionCreate, embed *discordgo.MessageEmbed, ephemeral bool) {
	data := &discordgo.InteractionResponseData{Embeds: []*discordgo.MessageEmbed{embed}, AllowedMentions: &discordgo.MessageAllowedMentions{}}
	if ephemeral {
		data.Flags = discordgo.MessageFlagsEphemeral
	}
	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{Type: discordgo.InteractionResponseChannelMessageWithSource, Data: data}); err != nil {
		appLogger.Warnf("Could not answer /%s: %v", i.ApplicationCommandData().Name, err)
	}
}

func timestampField(name string, t time.Time) *discordgo.MessageEmbedField {
	return &discordgo.MessageEmbedField{Name: name, Value: fmt.Sprintf("<t:%d:D> (<t:%d:R>)", t.Unix(), t.Unix()), Inline: true}
}

func handleServerInfoCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if i.GuildID == "" {
		respondEphemeral(s, i, "This only works in a server.")
		return
	}
	g, err := s.GuildWithCounts(i.GuildID)
	if err != nil {
		respondEphemeral(s, i, errorText("/serverinfo", err))
		return
	}
	channels, _ := s.GuildChannels(i.GuildID)
	text, voice, categories := 0, 0, 0
	for _, c := range channels {
		switch c.Type {
		case discordgo.ChannelTypeGuildCategory:
			categories++
		case discordgo.ChannelTypeGuildVoice, discordgo.ChannelTypeGuildStageVoice:
			voice++
		default:
			text++
		}
	}
	created := accountCreated(g.ID)
	embed := &discordgo.MessageEmbed{
		Title:     g.Name,
		Color:     ticketColor,
		Thumbnail: &discordgo.MessageEmbedThumbnail{URL: g.IconURL("256")},
		Fields: []*discordgo.MessageEmbedField{
			{Name: "Owner", Value: "<@" + g.OwnerID + ">", Inline: true},
			timestampField("Created", created),
			{Name: "Members", Value: fmt.Sprintf("%d (%d online)", g.ApproximateMemberCount, g.ApproximatePresenceCount), Inline: true},
			{Name: "Channels", Value: fmt.Sprintf("%d text · %d voice · %d categories", text, voice, categories), Inline: true},
			{Name: "Roles", Value: fmt.Sprint(len(g.Roles)), Inline: true},
			{Name: "Emojis", Value: fmt.Sprint(len(g.Emojis)), Inline: true},
			{Name: "Boosts", Value: fmt.Sprintf("Level %d · %d boosts", g.PremiumTier, g.PremiumSubscriptionCount), Inline: true},
		},
		Footer: &discordgo.MessageEmbedFooter{Text: "Server ID " + g.ID},
	}
	if g.Description != "" {
		embed.Description = g.Description
	}
	respondEmbed(s, i, embed, false)
}

// sortedRoleMentions lists roles highest first, leaving out @everyone.
func sortedRoleMentions(guildID string, roleIDs []string, roles map[string]*discordgo.Role) string {
	ids := []string{}
	for _, id := range roleIDs {
		if id != guildID {
			ids = append(ids, id)
		}
	}
	sort.SliceStable(ids, func(a, b int) bool {
		var pa, pb int
		if r, ok := roles[ids[a]]; ok {
			pa = r.Position
		}
		if r, ok := roles[ids[b]]; ok {
			pb = r.Position
		}
		return pa > pb
	})
	if len(ids) == 0 {
		return "none"
	}
	text := mentionAll(ids)
	if len(text) > 1000 {
		return fmt.Sprintf("%d roles", len(ids))
	}
	return text
}

func handleUserInfoCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if i.GuildID == "" {
		respondEphemeral(s, i, "This only works in a server.")
		return
	}
	_, opts := commandOptions(i)
	user := interactionUser(i)
	if u := optUser(i, opts, "member"); u != nil {
		user = u
	}
	embed := &discordgo.MessageEmbed{
		Color:  ticketColor,
		Footer: &discordgo.MessageEmbedFooter{Text: "User ID " + user.ID},
	}
	member, err := s.GuildMember(i.GuildID, user.ID)
	if err == nil && member.User != nil {
		user = member.User
	}
	embed.Title = user.Username
	if user.GlobalName != "" && user.GlobalName != user.Username {
		embed.Title = user.GlobalName + " (" + user.Username + ")"
	}
	if user.Bot {
		embed.Title += " 🤖"
	}
	embed.Thumbnail = &discordgo.MessageEmbedThumbnail{URL: user.AvatarURL("256")}
	embed.Fields = append(embed.Fields, timestampField("Account made", accountCreated(user.ID)))
	if err != nil {
		embed.Description = "Not a member of this server."
		respondEmbed(s, i, embed, false)
		return
	}
	embed.Fields = append(embed.Fields, timestampField("Joined", member.JoinedAt))
	if member.Nick != "" {
		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{Name: "Nickname", Value: member.Nick, Inline: true})
	}
	if member.PremiumSince != nil {
		embed.Fields = append(embed.Fields, timestampField("Boosting since", *member.PremiumSince))
	}
	if member.CommunicationDisabledUntil != nil && member.CommunicationDisabledUntil.After(time.Now()) {
		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{Name: "Timed out until",
			Value: fmt.Sprintf("<t:%d:f>", member.CommunicationDisabledUntil.Unix()), Inline: true})
	}
	roles := map[string]*discordgo.Role{}
	if facts, err := loadRoleFacts(s, i.GuildID); err == nil {
		roles = facts.Roles
		if top := topRole(member.Roles, roles); top != nil && top.Color != 0 {
			embed.Color = top.Color
		}
	}
	embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
		Name: fmt.Sprintf("Roles (%d)", len(member.Roles)), Value: sortedRoleMentions(i.GuildID, member.Roles, roles)})
	// Staff also see how many moderation cases the member has.
	if servers != nil && hasPermission(i, permModerate) {
		if cases, err := servers.Cases(i.GuildID, user.ID, 0); err == nil && len(cases) > 0 {
			embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{Name: "Moderation",
				Value: fmt.Sprintf("%d case%s (`/cases`)", len(cases), plural(len(cases))), Inline: true})
		}
	}
	respondEmbed(s, i, embed, false)
}

func topRole(roleIDs []string, roles map[string]*discordgo.Role) *discordgo.Role {
	var top *discordgo.Role
	for _, id := range roleIDs {
		if r, ok := roles[id]; ok && (top == nil || r.Position > top.Position) {
			top = r
		}
	}
	return top
}

func handleAvatarCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	_, opts := commandOptions(i)
	user := interactionUser(i)
	if u := optUser(i, opts, "user"); u != nil {
		user = u
	}
	if user.Avatar == "" && user.Username == "" {
		if full, err := s.User(user.ID); err == nil {
			user = full
		}
	}
	embed := &discordgo.MessageEmbed{
		Title: user.Username + "'s avatar",
		Color: ticketColor,
		Image: &discordgo.MessageEmbedImage{URL: user.AvatarURL("1024")},
	}
	// A server-specific avatar, when they have one, is shown as well.
	if i.GuildID != "" {
		if m, err := s.GuildMember(i.GuildID, user.ID); err == nil && m.Avatar != "" {
			m.GuildID = i.GuildID
			embed.Description = fmt.Sprintf("[Server avatar](%s) · [Main avatar](%s)", m.AvatarURL("1024"), user.AvatarURL("1024"))
			embed.Image.URL = m.AvatarURL("1024")
		}
	}
	respondEmbed(s, i, embed, false)
}

// permissionNames lists the notable permissions a role carries.
func permissionNames(perms int64) []string {
	if perms&discordgo.PermissionAdministrator != 0 {
		return []string{"Administrator (every permission)"}
	}
	names := []string{}
	for _, p := range riskyPermissions {
		if perms&p.bit != 0 {
			names = append(names, p.name)
		}
	}
	return names
}

func handleRoleInfoCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if i.GuildID == "" {
		respondEphemeral(s, i, "This only works in a server.")
		return
	}
	_, opts := commandOptions(i)
	role := optRole(i, opts, "role")
	if facts, err := loadRoleFacts(s, i.GuildID); err == nil {
		if full, ok := facts.Roles[role.ID]; ok {
			role = full
		}
	}
	color := "none"
	if role.Color != 0 {
		color = fmt.Sprintf("#%06X", role.Color)
	}
	yesNo := func(b bool) string {
		if b {
			return "Yes"
		}
		return "No"
	}
	perms := permissionNames(role.Permissions)
	permText := "No staff permissions"
	if len(perms) > 0 {
		permText = strings.Join(perms, ", ")
	}
	fields := []*discordgo.MessageEmbedField{
		{Name: "Color", Value: color, Inline: true},
		{Name: "Position", Value: fmt.Sprint(role.Position), Inline: true},
		{Name: "Shown separately", Value: yesNo(role.Hoist), Inline: true},
		{Name: "Mentionable", Value: yesNo(role.Mentionable), Inline: true},
		{Name: "Managed by an app", Value: yesNo(role.Managed), Inline: true},
		timestampField("Created", accountCreated(role.ID)),
		{Name: "Staff permissions", Value: permText},
	}
	respondEmbed(s, i, &discordgo.MessageEmbed{
		Title: role.Name, Description: roleMention(role.ID), Color: role.Color, Fields: fields,
		Footer: &discordgo.MessageEmbedFooter{Text: "Role ID " + role.ID},
	}, false)
}

// ============================================================================
// HELP
// ============================================================================

// staffOnlyCommands check staff access inside the handler instead of with a default permission.
var staffOnlyCommands = map[string]bool{"crash": true, "marketing": true}

type helpEntry struct {
	Name, Description string
	Subcommands       []string
	Staff             bool
}

// helpEntries describes the registered commands that the member can use, sorted by name.
func helpEntries(commands map[string]*SlashCommandHandler, canUse func(c *SlashCommandHandler) bool) []helpEntry {
	out := []helpEntry{}
	for name, c := range commands {
		if !canUse(c) {
			continue
		}
		e := helpEntry{Name: name, Description: c.Description, Staff: c.DefaultPermissions != nil || staffOnlyCommands[name]}
		for _, o := range c.Options {
			switch o.Type {
			case discordgo.ApplicationCommandOptionSubCommand:
				e.Subcommands = append(e.Subcommands, o.Name)
			case discordgo.ApplicationCommandOptionSubCommandGroup:
				for _, inner := range o.Options {
					e.Subcommands = append(e.Subcommands, o.Name+" "+inner.Name)
				}
			}
		}
		out = append(out, e)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Name < out[b].Name })
	return out
}

func helpLine(e helpEntry) string {
	line := fmt.Sprintf("`/%s` %s", e.Name, e.Description)
	if len(e.Subcommands) > 0 {
		line += " · " + truncateRunes(strings.Join(e.Subcommands, ", "), 200)
	}
	return line + "\n"
}

// helpFields splits lines into embed fields of at most 1024 characters.
func helpFields(title string, lines []string) []*discordgo.MessageEmbedField {
	fields := []*discordgo.MessageEmbedField{}
	var b strings.Builder
	flush := func() {
		if b.Len() == 0 {
			return
		}
		name := title
		if len(fields) > 0 {
			name = title + " (continued)"
		}
		fields = append(fields, &discordgo.MessageEmbedField{Name: name, Value: b.String()})
		b.Reset()
	}
	for _, l := range lines {
		if b.Len()+len(l) > 1024 {
			flush()
		}
		b.WriteString(l)
	}
	flush()
	return fields
}

func handleHelpCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	commands := map[string]*SlashCommandHandler{}
	if CommandManager != nil {
		for _, name := range CommandManager.ListCommands() {
			if c, ok := CommandManager.GetCommand(name); ok {
				commands[name] = c
			}
		}
	}
	canUse := func(c *SlashCommandHandler) bool {
		if c.Name == "test" {
			return false
		}
		if staffOnlyCommands[c.Name] {
			return hasStaffAccess(s, i)
		}
		if c.DefaultPermissions != nil {
			return hasPermission(i, *c.DefaultPermissions)
		}
		return true
	}
	var everyone, staff []string
	for _, e := range helpEntries(commands, canUse) {
		if e.Staff {
			staff = append(staff, helpLine(e))
		} else {
			everyone = append(everyone, helpLine(e))
		}
	}
	fields := helpFields("For everyone", everyone)
	fields = append(fields, helpFields("Staff tools you can use", staff)...)
	embed := &discordgo.MessageEmbed{
		Title:       "🤖 Blazium Bot commands",
		Description: "Type `/` and a command's name to see its options.",
		Color:       0x00ff00,
		Fields:      fields,
		Footer:      &discordgo.MessageEmbedFooter{Text: "Only the commands you can use are listed"},
	}
	respondEmbed(s, i, embed, true)
}
