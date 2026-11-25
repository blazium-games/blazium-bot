package main

import (
	"fmt"
	"strings"

	"github.com/bwmarrin/discordgo"
)

// /settings: one place for a server's log channels, welcome and goodbye messages and
// auto-roles. Shown by default to Manage Server, like the other setup commands.
//
//	/settings show
//	/settings logs      mod_log, member_log, message_log, or turn one off
//	/settings welcome   channel and message for new members (with a test send)
//	/settings goodbye   channel and message for members who leave
//	/settings autorole add|remove   roles every new member gets

const (
	welcomeMessageMax     = 1500
	maxAutoRoles          = 10
	defaultWelcomeMessage = "Welcome to **{server}**, {user}! You're member #{count}."
	defaultGoodbyeMessage = "**{username}** has left the server."
)

func settingsCommand() *SlashCommandHandler {
	logChannels := []discordgo.ChannelType{discordgo.ChannelTypeGuildText}
	off := strOpt("turn_off", "Stop one of the logs", false, 0)
	off.Choices = []*discordgo.ApplicationCommandOptionChoice{
		{Name: "Mod log", Value: "mod"}, {Name: "Member log", Value: "member"}, {Name: "Message log", Value: "message"}, {Name: "All logs", Value: "all"},
	}
	greeting := func(name, who string) *discordgo.ApplicationCommandOption {
		return sub(name, "Set up the message posted when a member "+who,
			channelOpt("channel", "Where to post it", false, panelTextChannels...),
			strOpt("message", "The message. Use {user}, {username}, {server} and {count}", false, welcomeMessageMax),
			boolOpt("off", "Turn it off"),
			boolOpt("test", "Post a test message as if you had just "+who),
		)
	}
	return &SlashCommandHandler{
		Name: "settings", Description: "Set up log channels, welcome and goodbye messages and auto-roles",
		Handler: handleSettingsCommand, DefaultPermissions: &permManageGuild,
		Options: []*discordgo.ApplicationCommandOption{
			sub("show", "Show this server's settings"),
			sub("logs", "Pick log channels. Only the options you fill in change.",
				channelOpt("mod_log", "Warnings, timeouts, kicks, bans, purges and locks", false, logChannels...),
				channelOpt("member_log", "Members joining and leaving", false, logChannels...),
				channelOpt("message_log", "Deleted and edited messages (keep it staff-only)", false, logChannels...),
				off,
				boolOpt("allow_public", "Allow a message log everyone can see (not recommended)"),
			),
			greeting("welcome", "joined"),
			greeting("goodbye", "left"),
			{
				Type: discordgo.ApplicationCommandOptionSubCommandGroup, Name: "autorole", Description: "Roles every new member gets",
				Options: []*discordgo.ApplicationCommandOption{
					sub("add", "Give a role to everyone who joins", roleOpt("role", "The role", true)),
					sub("remove", "Stop giving a role to new members", roleOpt("role", "The role", true)),
				},
			},
		},
	}
}

func registerServerSettings() {
	registerGuildCommand(settingsCommand())
}

// renderGreeting fills in a welcome or goodbye message's placeholders.
func renderGreeting(template string, user *discordgo.User, serverName string, count int) string {
	name := user.Username
	if user.GlobalName != "" {
		name = user.GlobalName
	}
	countText := "?"
	if count > 0 {
		countText = fmt.Sprint(count)
	}
	return strings.NewReplacer(
		"{user}", "<@"+user.ID+">",
		"{mention}", "<@"+user.ID+">",
		"{username}", name,
		"{server}", serverName,
		"{count}", countText,
	).Replace(template)
}

func channelOrOff(id string) string {
	if id == "" {
		return "off"
	}
	return "<#" + id + ">"
}

// describeSettings is /settings show.
func describeSettings(g *guildSettings) string {
	var b strings.Builder
	b.WriteString("**Logs**\n")
	fmt.Fprintf(&b, "Mod log: %s\nMember log: %s\nMessage log: %s\n\n", channelOrOff(g.ModLogChannel), channelOrOff(g.MemberLogChannel), channelOrOff(g.MessageLogChannel))
	fmt.Fprintf(&b, "**Welcome**: %s\n", channelOrOff(g.WelcomeChannel))
	if g.WelcomeChannel != "" {
		fmt.Fprintf(&b, "> %s\n", strings.ReplaceAll(orDefault(g.WelcomeMessage, defaultWelcomeMessage), "\n", "\n> "))
	}
	fmt.Fprintf(&b, "**Goodbye**: %s\n", channelOrOff(g.GoodbyeChannel))
	if g.GoodbyeChannel != "" {
		fmt.Fprintf(&b, "> %s\n", strings.ReplaceAll(orDefault(g.GoodbyeMessage, defaultGoodbyeMessage), "\n", "\n> "))
	}
	b.WriteString("**Auto-roles**: ")
	if len(g.AutoRoles) == 0 {
		b.WriteString("none")
	} else {
		b.WriteString(mentionAll(g.AutoRoles))
	}
	if (g.MemberLogChannel != "" || g.WelcomeChannel != "" || g.GoodbyeChannel != "" || len(g.AutoRoles) > 0) && !memberEventsEnabled() {
		b.WriteString("\n\n⚠️ Joins and leaves aren't being received: the bot's host must turn on the Server Members intent " +
			"(Discord Developer Portal, and DISCORD_MEMBERS_INTENT=true).")
	}
	if g.MessageLogChannel != "" && !messageContentEnabled() {
		b.WriteString("\n\n⚠️ Without the Message Content intent (DISCORD_MESSAGE_CONTENT_INTENT=true), the message log can't show what was said.")
	}
	return b.String()
}

func orDefault(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

func removeString(list []string, v string) ([]string, bool) {
	out := list[:0:0]
	found := false
	for _, x := range list {
		if x == v {
			found = true
			continue
		}
		out = append(out, x)
	}
	return out, found
}

func handleSettingsCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if i.GuildID == "" || !canManageServer(i) {
		respondEphemeral(s, i, "You need the **Manage Server** permission for this.")
		return
	}
	if servers == nil {
		respondEphemeral(s, i, "Settings need the database, which isn't available right now.")
		return
	}
	if err := deferEphemeral(s, i); err != nil {
		appLogger.Warnf("Could not defer /settings: %v", err)
		return
	}
	sub, opts := commandOptions(i)
	msg, err := runSettings(s, i, sub, opts)
	if err != nil {
		msg = errorText("/settings "+sub, err)
	}
	editResponse(s, i, truncateRunes(msg, 2000))
}

func runSettings(s *discordgo.Session, i *discordgo.InteractionCreate, sub string, opts cmdOpts) (string, error) {
	g, err := servers.Settings(i.GuildID)
	if err != nil {
		return "", err
	}
	switch sub {
	case "show":
		return describeSettings(g), nil
	case "logs":
		return settingsLogs(s, i, g, opts)
	case "welcome", "goodbye":
		return settingsGreeting(s, i, g, sub, opts)
	case "autorole add", "autorole remove":
		return settingsAutoRole(s, i, g, sub, opts)
	}
	return "", userErrorf("Unknown setting.")
}

func settingsLogs(s *discordgo.Session, i *discordgo.InteractionCreate, g *guildSettings, opts cmdOpts) (string, error) {
	allowPublic, _ := optBool(opts, "allow_public")
	changed := []string{}
	set := func(option, label string, target *string, staffOnly bool) error {
		ch := optChannel(i, opts, option)
		if ch == nil {
			return nil
		}
		if err := checkPanelChannel(s, ch.ID); err != nil {
			return err
		}
		if staffOnly && !allowPublic && everyoneCanSee(s, i.GuildID, ch.ID) {
			return userErrorf("Everyone can see <#%s>, and the message log shows deleted messages. Pick a staff-only channel, or add `allow_public:True` if you're sure.", ch.ID)
		}
		*target = ch.ID
		changed = append(changed, fmt.Sprintf("%s: <#%s>", label, ch.ID))
		return nil
	}
	if err := set("mod_log", "Mod log", &g.ModLogChannel, false); err != nil {
		return "", err
	}
	if err := set("member_log", "Member log", &g.MemberLogChannel, false); err != nil {
		return "", err
	}
	if err := set("message_log", "Message log", &g.MessageLogChannel, true); err != nil {
		return "", err
	}
	switch off, _ := optString(opts, "turn_off"); off {
	case "mod":
		g.ModLogChannel = ""
		changed = append(changed, "Mod log: off")
	case "member":
		g.MemberLogChannel = ""
		changed = append(changed, "Member log: off")
	case "message":
		g.MessageLogChannel = ""
		changed = append(changed, "Message log: off")
	case "all":
		g.ModLogChannel, g.MemberLogChannel, g.MessageLogChannel = "", "", ""
		changed = append(changed, "All logs: off")
	}
	if len(changed) == 0 {
		return "", userErrorf("Pick a channel for at least one log, or a log to turn off.")
	}
	if err := saveServerSettings(g); err != nil {
		return "", err
	}
	msg := "Saved.\n" + strings.Join(changed, "\n")
	if g.MemberLogChannel != "" && !memberEventsEnabled() {
		msg += "\n\n⚠️ The member log needs the Server Members intent, which isn't on for this bot yet. Ask whoever hosts it."
	}
	return msg, nil
}

func settingsGreeting(s *discordgo.Session, i *discordgo.InteractionCreate, g *guildSettings, which string, opts cmdOpts) (string, error) {
	channel, message := &g.WelcomeChannel, &g.WelcomeMessage
	fallback := defaultWelcomeMessage
	if which == "goodbye" {
		channel, message = &g.GoodbyeChannel, &g.GoodbyeMessage
		fallback = defaultGoodbyeMessage
	}
	changed := false
	if off, _ := optBool(opts, "off"); off {
		*channel = ""
		changed = true
	}
	if ch := optChannel(i, opts, "channel"); ch != nil {
		if err := checkPanelChannel(s, ch.ID); err != nil {
			return "", err
		}
		*channel = ch.ID
		changed = true
	}
	if text, ok := optString(opts, "message"); ok {
		*message = strings.ReplaceAll(text, `\n`, "\n")
		changed = true
	}
	test, _ := optBool(opts, "test")
	if !changed && !test {
		return "", userErrorf("Pick a channel, write a message, turn it off, or send a test.")
	}
	if changed {
		if err := saveServerSettings(g); err != nil {
			return "", err
		}
	}
	if *channel == "" {
		if _, wrote := optString(opts, "message"); wrote {
			return fmt.Sprintf("Saved the %s message. It's off until you pick a channel for it.", which), nil
		}
		return fmt.Sprintf("The %s message is off.", which), nil
	}
	preview := renderGreeting(orDefault(*message, fallback), interactionUser(i), guildName(s, i.GuildID), guildMemberCount(s, i.GuildID))
	msg := fmt.Sprintf("The %s message goes to <#%s>:\n> %s", which, *channel, strings.ReplaceAll(preview, "\n", "\n> "))
	if test {
		if err := sendGreeting(s, *channel, preview, interactionUser(i).ID); err != nil {
			return "", discordRefusal("post there", err)
		}
		msg += "\n\nPosted a test."
	}
	if !memberEventsEnabled() {
		msg += "\n\n⚠️ It won't be sent until the bot's host turns on the Server Members intent (Discord Developer Portal, and DISCORD_MEMBERS_INTENT=true)."
	}
	return msg, nil
}

func settingsAutoRole(s *discordgo.Session, i *discordgo.InteractionCreate, g *guildSettings, sub string, opts cmdOpts) (string, error) {
	role := optRole(i, opts, "role")
	if sub == "autorole remove" {
		var found bool
		g.AutoRoles, found = removeString(g.AutoRoles, role.ID)
		if !found {
			return "", userErrorf("%s isn't an auto-role.", roleMention(role.ID))
		}
		if err := saveServerSettings(g); err != nil {
			return "", err
		}
		return fmt.Sprintf("New members no longer get %s.", roleMention(role.ID)), nil
	}
	for _, r := range g.AutoRoles {
		if r == role.ID {
			return "", userErrorf("%s is already an auto-role.", roleMention(role.ID))
		}
	}
	if len(g.AutoRoles) >= maxAutoRoles {
		return "", userErrorf("A server can have up to %d auto-roles.", maxAutoRoles)
	}
	facts, err := loadRoleFacts(s, i.GuildID)
	if err != nil {
		return "", err
	}
	if full, ok := facts.Roles[role.ID]; ok {
		role = full
	}
	if why := roleProblem(role, facts); why != "" {
		return "", userErrorf("I can't give out %s: %s.", roleMention(role.ID), why)
	}
	g.AutoRoles = append(g.AutoRoles, role.ID)
	if err := saveServerSettings(g); err != nil {
		return "", err
	}
	msg := fmt.Sprintf("Everyone who joins now gets %s.", roleMention(role.ID))
	if !memberEventsEnabled() {
		msg += "\n\n⚠️ This needs the Server Members intent, which isn't on for this bot yet. Ask whoever hosts it."
	}
	return msg, nil
}

func guildName(s *discordgo.Session, guildID string) string {
	if g, err := lookupGuild(s, guildID); err == nil {
		return g.Name
	}
	return "the server"
}

func guildMemberCount(s *discordgo.Session, guildID string) int {
	if g, err := s.State.Guild(guildID); err == nil && g.MemberCount > 0 {
		return g.MemberCount
	}
	if g, err := s.GuildWithCounts(guildID); err == nil {
		return g.ApproximateMemberCount
	}
	return 0
}

// sendGreeting posts a welcome or goodbye message that pings only the member it's about.
func sendGreeting(s *discordgo.Session, channelID, text, userID string) error {
	_, err := s.ChannelMessageSendComplex(channelID, &discordgo.MessageSend{
		Content:         truncateRunes(text, 2000),
		AllowedMentions: &discordgo.MessageAllowedMentions{Users: []string{userID}},
	})
	return err
}
