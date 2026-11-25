package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/bwmarrin/discordgo"
)

// Moderation: warnings, timeouts, kicks and bans with numbered cases, plus channel tools.
//
//	/warn /timeout /untimeout /kick /ban /unban   act on a member; each one is a case
//	/cases [member]                               a member's history, or the latest cases
//	/case show|reason|delete                      one case
//	/purge /slowmode /lock /unlock                tidy or calm a channel
//
// Each command is shown by default only to members holding the matching Discord
// permission (Moderate Members, Kick Members…) and checks it again when used, because
// admins can open commands to anyone in Server Settings > Integrations. Every action is
// written to the server's mod log channel (/settings logs) and, where it can be, sent to
// the member by DM. Members whose highest role is level with or above the moderator's
// (or the bot's) can't be acted on.

const (
	modColor        = 0xED4245
	modUndoColor    = 0x57F287
	maxTimeout      = 28 * 24 * time.Hour // Discord's limit
	maxSlowmode     = 6 * time.Hour       // Discord's limit
	auditReasonMax  = 400
	modReasonMax    = 500
	casesPerListing = 15
)

var (
	permModerate       = int64(discordgo.PermissionModerateMembers)
	permKick           = int64(discordgo.PermissionKickMembers)
	permBan            = int64(discordgo.PermissionBanMembers)
	permManageMessages = int64(discordgo.PermissionManageMessages)
	permManageChannels = int64(discordgo.PermissionManageChannels)
)

func initServerTools() {
	db := botDatabase()
	if db == nil {
		appLogger.Warn("Postgres is unavailable; server settings and moderation history disabled")
		return
	}
	store, err := newPostgresServerStore(db)
	if err != nil {
		appLogger.Errorf("Server settings and moderation history disabled: %v", err)
		return
	}
	servers = store
	appLogger.Info("Server settings and moderation ready")
}

// ============================================================================
// COMMAND DEFINITIONS
// ============================================================================

func reasonOpt() *discordgo.ApplicationCommandOption {
	return strOpt("reason", "Why (shown in the mod log, the audit log and the member's DM)", false, modReasonMax)
}

func silentOpt() *discordgo.ApplicationCommandOption {
	return boolOpt("silent", "Don't DM the member about it")
}

func caseNumberOpt() *discordgo.ApplicationCommandOption {
	return intOpt("number", "The case number", true, 1, 1e12)
}

func moderationCommands() []*SlashCommandHandler {
	deleteChoices := []*discordgo.ApplicationCommandOptionChoice{
		{Name: "None", Value: 0}, {Name: "The last day", Value: 1}, {Name: "The last 3 days", Value: 3}, {Name: "The last 7 days", Value: 7},
	}
	deleteOpt := &discordgo.ApplicationCommandOption{Type: discordgo.ApplicationCommandOptionInteger, Name: "delete_messages",
		Description: "Also delete their recent messages", Choices: deleteChoices}
	text := []discordgo.ChannelType{discordgo.ChannelTypeGuildText, discordgo.ChannelTypeGuildNews, discordgo.ChannelTypeGuildForum,
		discordgo.ChannelTypeGuildVoice}
	return []*SlashCommandHandler{
		{Name: "warn", Description: "Warn a member and add it to their history", Handler: handleWarnCommand, DefaultPermissions: &permModerate,
			Options: []*discordgo.ApplicationCommandOption{userOpt("member", "Who to warn", true),
				strOpt("reason", "Why (sent to the member and kept in their history)", true, modReasonMax), silentOpt()}},
		{Name: "timeout", Description: "Stop a member from talking for a while", Handler: handleTimeoutCommand, DefaultPermissions: &permModerate,
			Options: []*discordgo.ApplicationCommandOption{userOpt("member", "Who to time out", true),
				strOpt("duration", "How long, like 10m, 2h, 1d or 1w (28 days at most)", true, 20), reasonOpt(), silentOpt()}},
		{Name: "untimeout", Description: "End a member's timeout early", Handler: handleUntimeoutCommand, DefaultPermissions: &permModerate,
			Options: []*discordgo.ApplicationCommandOption{userOpt("member", "Whose timeout to end", true), reasonOpt()}},
		{Name: "kick", Description: "Remove a member from the server (they can rejoin with an invite)", Handler: handleKickCommand, DefaultPermissions: &permKick,
			Options: []*discordgo.ApplicationCommandOption{userOpt("member", "Who to kick", true), reasonOpt(), silentOpt()}},
		{Name: "ban", Description: "Ban someone from the server", Handler: handleBanCommand, DefaultPermissions: &permBan,
			Options: []*discordgo.ApplicationCommandOption{userOpt("user", "Who to ban (they don't have to be in the server)", true),
				reasonOpt(), deleteOpt, silentOpt()}},
		{Name: "unban", Description: "Lift a ban", Handler: handleUnbanCommand, DefaultPermissions: &permBan,
			Options: []*discordgo.ApplicationCommandOption{strOpt("user_id", "The banned user's ID", true, 25), reasonOpt()}},
		{Name: "cases", Description: "A member's moderation history, or the latest cases", Handler: handleCasesCommand, DefaultPermissions: &permModerate,
			Options: []*discordgo.ApplicationCommandOption{userOpt("member", "Whose history (everyone's latest cases if empty)", false)}},
		{Name: "case", Description: "Look at or change one moderation case", Handler: handleCaseCommand, DefaultPermissions: &permModerate,
			Options: []*discordgo.ApplicationCommandOption{
				sub("show", "Show a case", caseNumberOpt()),
				sub("reason", "Change a case's reason", caseNumberOpt(), strOpt("reason", "The new reason", true, modReasonMax)),
				sub("delete", "Delete a case given by mistake (the action itself isn't undone)", caseNumberOpt()),
			}},
		{Name: "purge", Description: "Delete recent messages in this channel", Handler: handlePurgeCommand, DefaultPermissions: &permManageMessages,
			Options: []*discordgo.ApplicationCommandOption{intOpt("amount", "How many of the latest messages to look through (1-100)", true, 1, 100),
				userOpt("member", "Only delete this member's messages", false),
				strOpt("containing", "Only delete messages containing this text", false, 100),
				boolOpt("bots", "Only delete messages from bots")}},
		{Name: "slowmode", Description: "Set how often each member may post in a channel", Handler: handleSlowmodeCommand, DefaultPermissions: &permManageChannels,
			Options: []*discordgo.ApplicationCommandOption{strOpt("delay", "Like 5s, 1m or 1h (6 hours at most); off to turn it off", true, 20),
				channelOpt("channel", "Which channel (this one if empty)", false, text...)}},
		{Name: "lock", Description: "Stop everyone without a staff role from posting in a channel", Handler: handleLockCommand, DefaultPermissions: &permManageChannels,
			Options: []*discordgo.ApplicationCommandOption{channelOpt("channel", "Which channel (this one if empty)", false, text...), reasonOpt()}},
		{Name: "unlock", Description: "Let everyone post in a locked channel again", Handler: handleUnlockCommand, DefaultPermissions: &permManageChannels,
			Options: []*discordgo.ApplicationCommandOption{channelOpt("channel", "Which channel (this one if empty)", false, text...), reasonOpt()}},
	}
}

func registerModeration() {
	for _, c := range moderationCommands() {
		registerGuildCommand(c)
	}
}

// registerGuildCommand registers a server-only command anyone may run (each handler checks permissions itself).
func registerGuildCommand(c *SlashCommandHandler) {
	if _, exists := CommandManager.GetCommand(c.Name); exists {
		return
	}
	c.Permissions = []string{"user"}
	c.GuildOnly = true
	if err := CommandManager.RegisterCommand(c.Name, c); err != nil {
		appLogger.Errorf("Failed to register %s command handler: %v", c.Name, err)
	}
}

// ============================================================================
// RULES (no Discord calls, so they can be tested)
// ============================================================================

// hasPermission: the member holds perm (or Administrator), or the STAFF_ROLE_ID role.
func hasPermission(i *discordgo.InteractionCreate, perm int64) bool {
	if i.Member == nil {
		return false
	}
	if i.Member.Permissions&(perm|discordgo.PermissionAdministrator) != 0 {
		return true
	}
	if staff := os.Getenv("STAFF_ROLE_ID"); staff != "" {
		for _, r := range i.Member.Roles {
			if r == staff {
				return true
			}
		}
	}
	return false
}

// parseDuration reads "90s", "10m", "1h30m", "2d", "1w" or a bare number of minutes.
func parseDuration(text string) (time.Duration, error) {
	text = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(text), " ", ""))
	if text == "" {
		return 0, userErrorf("Give a duration like 10m, 2h or 1d.")
	}
	if n, err := strconv.Atoi(text); err == nil {
		return time.Duration(n) * time.Minute, nil
	}
	units := map[string]time.Duration{
		"s": time.Second, "sec": time.Second, "secs": time.Second, "second": time.Second, "seconds": time.Second,
		"m": time.Minute, "min": time.Minute, "mins": time.Minute, "minute": time.Minute, "minutes": time.Minute,
		"h": time.Hour, "hr": time.Hour, "hrs": time.Hour, "hour": time.Hour, "hours": time.Hour,
		"d": 24 * time.Hour, "day": 24 * time.Hour, "days": 24 * time.Hour,
		"w": 7 * 24 * time.Hour, "wk": 7 * 24 * time.Hour, "week": 7 * 24 * time.Hour, "weeks": 7 * 24 * time.Hour,
	}
	var total time.Duration
	for text != "" {
		n := 0
		for n < len(text) && unicode.IsDigit(rune(text[n])) {
			n++
		}
		if n == 0 {
			return 0, userErrorf("I couldn't read %q as a duration. Try something like 10m, 2h or 1d.", text)
		}
		value, _ := strconv.Atoi(text[:n])
		text = text[n:]
		u := 0
		for u < len(text) && unicode.IsLetter(rune(text[u])) {
			u++
		}
		unit, ok := units[text[:u]]
		if !ok {
			return 0, userErrorf("I don't know the unit %q. Use s, m, h, d or w.", text[:u])
		}
		text = text[u:]
		total += time.Duration(value) * unit
		if total > 365*24*time.Hour {
			return 0, userErrorf("That's too long.")
		}
	}
	return total, nil
}

// formatDuration is a short, readable duration: "2d 3h", "45m", "30s".
func formatDuration(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	parts := []string{}
	for _, u := range []struct {
		size time.Duration
		name string
	}{{7 * 24 * time.Hour, "w"}, {24 * time.Hour, "d"}, {time.Hour, "h"}, {time.Minute, "m"}, {time.Second, "s"}} {
		if d >= u.size {
			parts = append(parts, fmt.Sprintf("%d%s", d/u.size, u.name))
			d %= u.size
		}
	}
	if len(parts) > 2 {
		parts = parts[:2]
	}
	return strings.Join(parts, " ")
}

// moderationTarget is what the hierarchy rule needs to know about the people involved.
type moderationTarget struct {
	ModeratorID    string
	ModeratorRoles []string
	TargetID       string
	TargetRoles    []string
	BotID          string
}

// moderationProblem is why the moderator (or the bot) can't act on the target; "" if they can.
func moderationProblem(t moderationTarget, f *guildRoleFacts) string {
	switch t.TargetID {
	case t.ModeratorID:
		return "You can't do that to yourself."
	case t.BotID:
		return "I can't do that to myself."
	case f.OwnerID:
		return "The server owner can't be moderated."
	}
	targetTop := topPosition(t.TargetRoles, f.Roles)
	if t.ModeratorID != f.OwnerID && topPosition(t.ModeratorRoles, f.Roles) <= targetTop {
		return "Their highest role is level with or above yours, so you can't moderate them."
	}
	if f.BotTop <= targetTop {
		return "Their highest role is level with or above mine, so Discord won't let me. Move my role higher in Server Settings > Roles."
	}
	return ""
}

func auditReason(moderator *discordgo.User, reason string) string {
	text := moderator.Username
	if reason != "" {
		text += ": " + reason
	}
	return truncateRunes(text, auditReasonMax)
}

// auditLog puts who did it and why in the server's audit log. Discord wants the header
// URL-encoded so that non-ASCII reasons survive.
func auditLog(moderator *discordgo.User, reason string) discordgo.RequestOption {
	return discordgo.WithAuditLogReason(url.PathEscape(auditReason(moderator, reason)))
}

func reasonOrNone(reason string) string {
	if strings.TrimSpace(reason) == "" {
		return "No reason given"
	}
	return reason
}

var caseVerbs = map[string]string{
	caseWarn: "Warned", caseTimeout: "Timed out", caseUntime: "Timeout ended", caseKick: "Kicked", caseBan: "Banned", caseUnban: "Unbanned",
}

var caseEmoji = map[string]string{
	caseWarn: "⚠️", caseTimeout: "🔇", caseUntime: "🔊", caseKick: "👢", caseBan: "🔨", caseUnban: "🕊️",
}

// caseEmbed is how a case looks in the mod log and in /case show.
func caseEmbed(c *modCase) *discordgo.MessageEmbed {
	color := modColor
	if c.Action == caseUnban || c.Action == caseUntime {
		color = modUndoColor
	} else if c.Action == caseWarn {
		color = colorNotice
	}
	fields := []*discordgo.MessageEmbedField{
		{Name: "Member", Value: fmt.Sprintf("<@%s> (`%s`)", c.UserID, c.UserID), Inline: true},
		{Name: "Moderator", Value: fmt.Sprintf("<@%s>", c.ModeratorID), Inline: true},
	}
	if c.Duration > 0 {
		fields = append(fields, &discordgo.MessageEmbedField{Name: "Duration", Value: formatDuration(c.Duration), Inline: true})
	}
	fields = append(fields, &discordgo.MessageEmbedField{Name: "Reason", Value: reasonOrNone(c.Reason)})
	return &discordgo.MessageEmbed{
		Title:     fmt.Sprintf("%s Case #%d · %s", caseEmoji[c.Action], c.Number, caseVerbs[c.Action]),
		Color:     color,
		Fields:    fields,
		Timestamp: c.CreatedAt.Format(time.RFC3339),
	}
}

// caseLine is one line of /cases.
func caseLine(c modCase, withMember bool) string {
	line := fmt.Sprintf("`#%d` %s **%s** <t:%d:R>", c.Number, caseEmoji[c.Action], caseVerbs[c.Action], c.CreatedAt.Unix())
	if withMember {
		line += fmt.Sprintf(" <@%s>", c.UserID)
	}
	if c.Duration > 0 {
		line += " for " + formatDuration(c.Duration)
	}
	line += " by <@" + c.ModeratorID + ">"
	if c.Reason != "" {
		line += ": " + truncateRunes(c.Reason, 80)
	}
	return line
}

// discordRefusal turns a Discord API error into something a moderator can act on.
func discordRefusal(action string, err error) error {
	var rest *discordgo.RESTError
	if errors.As(err, &rest) && rest.Response != nil {
		switch rest.Response.StatusCode {
		case http.StatusForbidden:
			return userErrorf("Discord didn't let me %s. Check that I have the permission for it and that my role is above theirs.", action)
		case http.StatusNotFound:
			return userErrorf("Discord couldn't find them (they may have left, or the ID is wrong).")
		}
	}
	return err
}

// ============================================================================
// SHARED STEPS
// ============================================================================

// optUser is a user option, from the data Discord sent with the command.
func optUser(i *discordgo.InteractionCreate, opts cmdOpts, name string) *discordgo.User {
	o, ok := opts[name]
	if !ok {
		return nil
	}
	id, _ := o.Value.(string)
	if r := i.ApplicationCommandData().Resolved; r != nil && r.Users != nil {
		if u, ok := r.Users[id]; ok {
			return u
		}
	}
	return &discordgo.User{ID: id}
}

// checkCanModerate fetches target and makes sure the moderator and bot may act on them.
// The target must be in the server.
func checkCanModerate(s *discordgo.Session, i *discordgo.InteractionCreate, targetID string) (*discordgo.Member, error) {
	member, err := s.GuildMember(i.GuildID, targetID)
	if err != nil {
		if isNotFound(err) {
			return nil, userErrorf("They aren't in this server.")
		}
		return nil, err
	}
	if err := checkHierarchy(s, i, targetID, member.Roles); err != nil {
		return nil, err
	}
	return member, nil
}

func checkHierarchy(s *discordgo.Session, i *discordgo.InteractionCreate, targetID string, targetRoles []string) error {
	facts, err := loadRoleFacts(s, i.GuildID)
	if err != nil {
		return err
	}
	if why := moderationProblem(moderationTarget{
		ModeratorID: interactionUser(i).ID, ModeratorRoles: i.Member.Roles,
		TargetID: targetID, TargetRoles: targetRoles, BotID: s.State.User.ID,
	}, facts); why != "" {
		return userErrorf("%s", why)
	}
	return nil
}

// recordCase saves a case (when the database is up) and posts it to the mod log.
// It returns the case, numbered if it was saved.
func recordCase(s *discordgo.Session, c *modCase) *modCase {
	c.CreatedAt = time.Now()
	if servers != nil {
		number, err := servers.AddCase(c)
		if err != nil {
			appLogger.Errorf("Could not save a %s case in %s: %v", c.Action, c.GuildID, err)
		} else {
			c.Number = number
		}
	}
	postModLog(s, c.GuildID, caseEmbed(c))
	return c
}

// postModLog posts embed to the server's mod log channel, if it has one.
func postModLog(s *discordgo.Session, guildID string, embed *discordgo.MessageEmbed) {
	postToLogChannel(s, guildID, func(g *guildSettings) string { return g.ModLogChannel }, embed)
}

func postToLogChannel(s *discordgo.Session, guildID string, channel func(*guildSettings) string, embed *discordgo.MessageEmbed) {
	g := serverSettings(guildID)
	if g == nil || channel(g) == "" {
		return
	}
	_, err := s.ChannelMessageSendComplex(channel(g), &discordgo.MessageSend{
		Embeds:          []*discordgo.MessageEmbed{embed},
		AllowedMentions: &discordgo.MessageAllowedMentions{},
	})
	if err != nil {
		appLogger.Warnf("Could not post to the log channel %s in %s: %v", channel(g), guildID, err)
	}
}

// dmMember tells a member what happened to them. It reports whether the DM arrived.
func dmMember(s *discordgo.Session, guildID, userID, what, reason string) bool {
	serverName := "the server"
	if g, err := lookupGuild(s, guildID); err == nil {
		serverName = g.Name
	}
	ch, err := s.UserChannelCreate(userID)
	if err != nil {
		return false
	}
	embed := &discordgo.MessageEmbed{
		Title:       fmt.Sprintf("You were %s in %s", what, serverName),
		Description: "**Reason:** " + reasonOrNone(reason),
		Color:       modColor,
	}
	_, err = s.ChannelMessageSendEmbed(ch.ID, embed)
	return err == nil
}

func dmNote(silent, delivered bool) string {
	switch {
	case silent:
		return ""
	case delivered:
		return " They were told by DM."
	default:
		return " I couldn't DM them (their DMs are closed)."
	}
}

func caseLabel(c *modCase) string {
	if c.Number == 0 {
		return ""
	}
	return fmt.Sprintf(" (case #%d)", c.Number)
}

// startModeration runs the common opening of a moderation command: the permission check
// and "thinking…". work returns the message to show the moderator.
func startModeration(s *discordgo.Session, i *discordgo.InteractionCreate, perm int64, permName string, work func(opts cmdOpts) (string, error)) {
	if i.GuildID == "" || i.Member == nil {
		respondEphemeral(s, i, "This only works in a server.")
		return
	}
	if !hasPermission(i, perm) {
		respondEphemeral(s, i, "You need the **"+permName+"** permission for this.")
		return
	}
	if err := deferEphemeral(s, i); err != nil {
		appLogger.Warnf("Could not defer /%s: %v", i.ApplicationCommandData().Name, err)
		return
	}
	_, opts := commandOptions(i)
	msg, err := work(opts)
	if err != nil {
		msg = errorText("/"+i.ApplicationCommandData().Name, err)
	}
	editResponse(s, i, msg)
}

// ============================================================================
// MEMBER ACTIONS
// ============================================================================

func handleWarnCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	startModeration(s, i, permModerate, "Timeout Members", func(opts cmdOpts) (string, error) {
		target := optUser(i, opts, "member")
		reason, _ := optString(opts, "reason")
		silent, _ := optBool(opts, "silent")
		if target.Bot {
			return "", userErrorf("Bots can't be warned.")
		}
		if _, err := checkCanModerate(s, i, target.ID); err != nil {
			return "", err
		}
		c := recordCase(s, &modCase{GuildID: i.GuildID, Action: caseWarn, UserID: target.ID, ModeratorID: interactionUser(i).ID, Reason: reason})
		delivered := !silent && dmMember(s, i.GuildID, target.ID, "warned", reason)
		total := ""
		if servers != nil {
			if all, err := servers.Cases(i.GuildID, target.ID, 0); err == nil {
				warns := 0
				for _, x := range all {
					if x.Action == caseWarn {
						warns++
					}
				}
				total = fmt.Sprintf(" They now have %d warning%s.", warns, plural(warns))
			}
		}
		return fmt.Sprintf("⚠️ Warned <@%s>%s.%s%s", target.ID, caseLabel(c), total, dmNote(silent, delivered)), nil
	})
}

func handleTimeoutCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	startModeration(s, i, permModerate, "Timeout Members", func(opts cmdOpts) (string, error) {
		target := optUser(i, opts, "member")
		text, _ := optString(opts, "duration")
		reason, _ := optString(opts, "reason")
		silent, _ := optBool(opts, "silent")
		d, err := parseDuration(text)
		if err != nil {
			return "", err
		}
		if d < time.Minute {
			return "", userErrorf("A timeout must be at least a minute.")
		}
		if d > maxTimeout {
			return "", userErrorf("Discord allows timeouts of up to 28 days.")
		}
		if _, err := checkCanModerate(s, i, target.ID); err != nil {
			return "", err
		}
		until := time.Now().Add(d)
		if err := s.GuildMemberTimeout(i.GuildID, target.ID, &until, auditLog(interactionUser(i), reason)); err != nil {
			return "", discordRefusal("time them out", err)
		}
		c := recordCase(s, &modCase{GuildID: i.GuildID, Action: caseTimeout, UserID: target.ID, ModeratorID: interactionUser(i).ID, Reason: reason, Duration: d})
		delivered := !silent && dmMember(s, i.GuildID, target.ID, "timed out for "+formatDuration(d), reason)
		return fmt.Sprintf("🔇 Timed out <@%s> until <t:%d:f>%s.%s", target.ID, until.Unix(), caseLabel(c), dmNote(silent, delivered)), nil
	})
}

func handleUntimeoutCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	startModeration(s, i, permModerate, "Timeout Members", func(opts cmdOpts) (string, error) {
		target := optUser(i, opts, "member")
		reason, _ := optString(opts, "reason")
		member, err := checkCanModerate(s, i, target.ID)
		if err != nil {
			return "", err
		}
		if member.CommunicationDisabledUntil == nil || member.CommunicationDisabledUntil.Before(time.Now()) {
			return "", userErrorf("<@%s> isn't timed out.", target.ID)
		}
		if err := s.GuildMemberTimeout(i.GuildID, target.ID, nil, auditLog(interactionUser(i), reason)); err != nil {
			return "", discordRefusal("end their timeout", err)
		}
		c := recordCase(s, &modCase{GuildID: i.GuildID, Action: caseUntime, UserID: target.ID, ModeratorID: interactionUser(i).ID, Reason: reason})
		return fmt.Sprintf("🔊 Ended <@%s>'s timeout%s.", target.ID, caseLabel(c)), nil
	})
}

func handleKickCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	startModeration(s, i, permKick, "Kick Members", func(opts cmdOpts) (string, error) {
		target := optUser(i, opts, "member")
		reason, _ := optString(opts, "reason")
		silent, _ := optBool(opts, "silent")
		if _, err := checkCanModerate(s, i, target.ID); err != nil {
			return "", err
		}
		// DM first: once they're gone, the bot shares no server with them.
		delivered := !silent && dmMember(s, i.GuildID, target.ID, "kicked", reason)
		if err := s.GuildMemberDelete(i.GuildID, target.ID, auditLog(interactionUser(i), reason)); err != nil {
			return "", discordRefusal("kick them", err)
		}
		c := recordCase(s, &modCase{GuildID: i.GuildID, Action: caseKick, UserID: target.ID, ModeratorID: interactionUser(i).ID, Reason: reason})
		return fmt.Sprintf("👢 Kicked %s%s.%s", userLabel(target), caseLabel(c), dmNote(silent, delivered)), nil
	})
}

func handleBanCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	startModeration(s, i, permBan, "Ban Members", func(opts cmdOpts) (string, error) {
		target := optUser(i, opts, "user")
		reason, _ := optString(opts, "reason")
		silent, _ := optBool(opts, "silent")
		days, _ := optInt(opts, "delete_messages")
		inServer := true
		member, err := s.GuildMember(i.GuildID, target.ID)
		if err != nil {
			if !isNotFound(err) {
				return "", err
			}
			inServer = false
		}
		if inServer {
			if err := checkHierarchy(s, i, target.ID, member.Roles); err != nil {
				return "", err
			}
		} else if target.ID == interactionUser(i).ID || target.ID == s.State.User.ID {
			return "", userErrorf("That isn't someone I can ban.")
		}
		delivered := false
		if inServer && !silent {
			delivered = dmMember(s, i.GuildID, target.ID, "banned", reason)
		}
		if err := banUser(s, i.GuildID, target.ID, int(days), auditLog(interactionUser(i), reason)); err != nil {
			return "", discordRefusal("ban them", err)
		}
		c := recordCase(s, &modCase{GuildID: i.GuildID, Action: caseBan, UserID: target.ID, ModeratorID: interactionUser(i).ID, Reason: reason})
		note := dmNote(silent, delivered)
		if !inServer {
			note = " They weren't in the server, so they weren't told."
		}
		return fmt.Sprintf("🔨 Banned %s%s.%s", userLabel(target), caseLabel(c), note), nil
	})
}

// banUser bans with the API's delete_message_seconds (discordgo still sends the retired
// delete_message_days) and puts the reason in the audit log header.
func banUser(s *discordgo.Session, guildID, userID string, deleteDays int, reason discordgo.RequestOption) error {
	body := map[string]int{}
	if deleteDays > 0 {
		body["delete_message_seconds"] = deleteDays * 24 * 60 * 60
	}
	_, err := s.RequestWithBucketID("PUT", discordgo.EndpointGuildBan(guildID, userID), body,
		discordgo.EndpointGuildBan(guildID, ""), reason)
	return err
}

func handleUnbanCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	startModeration(s, i, permBan, "Ban Members", func(opts cmdOpts) (string, error) {
		raw, _ := optString(opts, "user_id")
		userID := strings.Trim(strings.TrimSpace(raw), "<@!>")
		if _, err := strconv.ParseUint(userID, 10, 64); err != nil {
			return "", userErrorf("That isn't a user ID. Turn on Developer Mode, then right-click the user and Copy User ID.")
		}
		reason, _ := optString(opts, "reason")
		if _, err := s.GuildBan(i.GuildID, userID); err != nil {
			if isNotFound(err) {
				return "", userErrorf("<@%s> isn't banned.", userID)
			}
			return "", discordRefusal("look up the ban", err)
		}
		if err := s.GuildBanDelete(i.GuildID, userID, auditLog(interactionUser(i), reason)); err != nil {
			return "", discordRefusal("lift the ban", err)
		}
		c := recordCase(s, &modCase{GuildID: i.GuildID, Action: caseUnban, UserID: userID, ModeratorID: interactionUser(i).ID, Reason: reason})
		return fmt.Sprintf("🕊️ Unbanned <@%s>%s.", userID, caseLabel(c)), nil
	})
}

func userLabel(u *discordgo.User) string {
	if u.Username == "" {
		return "<@" + u.ID + ">"
	}
	return fmt.Sprintf("**%s** (<@%s>)", u.Username, u.ID)
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// ============================================================================
// CASES
// ============================================================================

func handleCasesCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	startModeration(s, i, permModerate, "Timeout Members", func(opts cmdOpts) (string, error) {
		if servers == nil {
			return "", userErrorf("Moderation history needs the database, which isn't available right now.")
		}
		userID := ""
		if u := optUser(i, opts, "member"); u != nil {
			userID = u.ID
		}
		cases, err := servers.Cases(i.GuildID, userID, 0)
		if err != nil {
			return "", err
		}
		if len(cases) == 0 {
			if userID != "" {
				return fmt.Sprintf("<@%s> has a clean record.", userID), nil
			}
			return "There are no moderation cases yet.", nil
		}
		var b strings.Builder
		if userID != "" {
			counts := map[string]int{}
			for _, c := range cases {
				counts[c.Action]++
			}
			summary := []string{}
			for _, a := range []string{caseWarn, caseTimeout, caseKick, caseBan} {
				if counts[a] > 0 {
					summary = append(summary, fmt.Sprintf("%d %s", counts[a], strings.ToLower(caseVerbs[a])))
				}
			}
			fmt.Fprintf(&b, "**History of <@%s>**: %s\n", userID, strings.Join(summary, ", "))
		} else {
			b.WriteString("**Latest cases**\n")
		}
		shown := cases
		if len(shown) > casesPerListing {
			shown = shown[:casesPerListing]
		}
		for _, c := range shown {
			b.WriteString(caseLine(c, userID == "") + "\n")
		}
		if len(cases) > len(shown) {
			fmt.Fprintf(&b, "…and %d older. Use `/case show` for any of them.", len(cases)-len(shown))
		}
		return truncateRunes(b.String(), 2000), nil
	})
}

func handleCaseCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if i.GuildID == "" || !hasPermission(i, permModerate) {
		respondEphemeral(s, i, "You need the **Timeout Members** permission for this.")
		return
	}
	if servers == nil {
		respondEphemeral(s, i, "Moderation history needs the database, which isn't available right now.")
		return
	}
	sub, opts := commandOptions(i)
	number, _ := optInt(opts, "number")
	c, err := servers.Case(i.GuildID, number)
	if err != nil {
		respondEphemeral(s, i, errorText("/case", err))
		return
	}
	if c == nil {
		respondEphemeral(s, i, fmt.Sprintf("There is no case #%d.", number))
		return
	}
	switch sub {
	case "show":
		err = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{Embeds: []*discordgo.MessageEmbed{caseEmbed(c)}, Flags: discordgo.MessageFlagsEphemeral},
		})
		if err != nil {
			appLogger.Warnf("Could not show case: %v", err)
		}
	case "reason":
		reason, _ := optString(opts, "reason")
		if _, err := servers.UpdateCaseReason(i.GuildID, number, reason); err != nil {
			respondEphemeral(s, i, errorText("/case reason", err))
			return
		}
		c.Reason = reason
		embed := caseEmbed(c)
		embed.Title += " · reason changed"
		embed.Footer = &discordgo.MessageEmbedFooter{Text: "Changed by " + interactionUser(i).Username}
		postModLog(s, i.GuildID, embed)
		respondEphemeral(s, i, fmt.Sprintf("Case #%d's reason is now: %s", number, reason))
	case "delete":
		if _, err := servers.DeleteCase(i.GuildID, number); err != nil {
			respondEphemeral(s, i, errorText("/case delete", err))
			return
		}
		postModLog(s, i.GuildID, &discordgo.MessageEmbed{
			Title:       fmt.Sprintf("🗑️ Case #%d deleted", number),
			Description: fmt.Sprintf("%s of <@%s> removed from their history by <@%s>.", caseVerbs[c.Action], c.UserID, interactionUser(i).ID),
			Color:       colorNotice,
		})
		respondEphemeral(s, i, fmt.Sprintf("Deleted case #%d. The %s itself wasn't undone.", number, strings.ToLower(caseVerbs[c.Action])))
	}
}

// ============================================================================
// CHANNEL TOOLS
// ============================================================================

// purgeFilter decides which of the fetched messages /purge deletes. Discord only bulk
// deletes messages younger than 14 days, so older ones are counted apart.
type purgeFilter struct {
	UserID     string
	Containing string
	BotsOnly   bool
	Now        time.Time
}

func (f purgeFilter) pick(msgs []*discordgo.Message) (ids []string, tooOld int) {
	limit := f.Now.Add(-14*24*time.Hour + time.Minute)
	needle := strings.ToLower(f.Containing)
	for _, m := range msgs {
		if m.Author == nil {
			continue
		}
		if f.UserID != "" && m.Author.ID != f.UserID {
			continue
		}
		if f.BotsOnly && !m.Author.Bot {
			continue
		}
		if needle != "" && !strings.Contains(strings.ToLower(m.Content), needle) {
			continue
		}
		if m.Timestamp.Before(limit) {
			tooOld++
			continue
		}
		ids = append(ids, m.ID)
	}
	return ids, tooOld
}

func handlePurgeCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	startModeration(s, i, permManageMessages, "Manage Messages", func(opts cmdOpts) (string, error) {
		amount, _ := optInt(opts, "amount")
		f := purgeFilter{Now: time.Now()}
		if u := optUser(i, opts, "member"); u != nil {
			f.UserID = u.ID
		}
		f.Containing, _ = optString(opts, "containing")
		f.BotsOnly, _ = optBool(opts, "bots")
		msgs, err := s.ChannelMessages(i.ChannelID, int(amount), "", "", "")
		if err != nil {
			return "", discordRefusal("read this channel", err)
		}
		ids, tooOld := f.pick(msgs)
		reason := auditLog(interactionUser(i), "/purge")
		switch {
		case len(ids) == 1:
			err = s.ChannelMessageDelete(i.ChannelID, ids[0], reason)
		case len(ids) > 1:
			err = s.ChannelMessagesBulkDelete(i.ChannelID, ids, reason)
		}
		if err != nil {
			return "", discordRefusal("delete messages here (I need Manage Messages)", err)
		}
		msg := fmt.Sprintf("🧹 Deleted %d message%s.", len(ids), plural(len(ids)))
		if tooOld > 0 {
			msg += fmt.Sprintf(" %d matching message%s were older than 14 days, which Discord doesn't let bots bulk delete.", tooOld, plural(tooOld))
		}
		if len(ids) > 0 {
			postModLog(s, i.GuildID, &discordgo.MessageEmbed{
				Title:       "🧹 Messages purged",
				Description: fmt.Sprintf("<@%s> deleted %d message%s in <#%s>.", interactionUser(i).ID, len(ids), plural(len(ids)), i.ChannelID),
				Color:       colorNotice,
				Timestamp:   time.Now().Format(time.RFC3339),
			})
		}
		return msg, nil
	})
}

// channelPermissions works out a member's permissions in one channel: their roles, then
// the channel's @everyone, role and member overwrites, as Discord does.
func channelPermissions(guildID, ownerID, userID string, memberRoles []string, roles map[string]*discordgo.Role, ch *discordgo.Channel) int64 {
	if userID == ownerID {
		return discordgo.PermissionAll
	}
	perms := guildPermissions(guildID, memberRoles, roles)
	if perms&discordgo.PermissionAdministrator != 0 {
		return discordgo.PermissionAll
	}
	has := map[string]bool{}
	for _, r := range memberRoles {
		has[r] = true
	}
	for _, o := range ch.PermissionOverwrites {
		if o.Type == discordgo.PermissionOverwriteTypeRole && o.ID == guildID {
			perms &^= o.Deny
			perms |= o.Allow
		}
	}
	var allow, deny int64
	for _, o := range ch.PermissionOverwrites {
		if o.Type == discordgo.PermissionOverwriteTypeRole && has[o.ID] {
			allow |= o.Allow
			deny |= o.Deny
		}
	}
	perms &^= deny
	perms |= allow
	for _, o := range ch.PermissionOverwrites {
		if o.Type == discordgo.PermissionOverwriteTypeMember && o.ID == userID {
			perms &^= o.Deny
			perms |= o.Allow
		}
	}
	return perms
}

// needChannelPermission makes sure the member may use perm in channelID. Discord only
// tells the bot the member's permissions in the channel the command was used in, so a
// command aimed at another channel (/lock #rules) is checked against that channel itself.
func needChannelPermission(s *discordgo.Session, i *discordgo.InteractionCreate, channelID string, perm int64, permName string) error {
	if channelID == i.ChannelID {
		return nil // checked by startModeration
	}
	ch, err := s.Channel(channelID)
	if err != nil || ch.GuildID != i.GuildID {
		return userErrorf("I can't see that channel.")
	}
	if hasPermission(i, discordgo.PermissionAdministrator) {
		return nil // Administrator or the staff role
	}
	facts, err := loadRoleFacts(s, i.GuildID)
	if err != nil {
		return err
	}
	if channelPermissions(i.GuildID, facts.OwnerID, interactionUser(i).ID, i.Member.Roles, facts.Roles, ch)&perm == 0 {
		return userErrorf("You need the **%s** permission in <#%s>.", permName, channelID)
	}
	return nil
}

func targetChannelID(i *discordgo.InteractionCreate, opts cmdOpts) string {
	if ch := optChannel(i, opts, "channel"); ch != nil {
		return ch.ID
	}
	return i.ChannelID
}

func handleSlowmodeCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	startModeration(s, i, permManageChannels, "Manage Channels", func(opts cmdOpts) (string, error) {
		channelID := targetChannelID(i, opts)
		if err := needChannelPermission(s, i, channelID, permManageChannels, "Manage Channels"); err != nil {
			return "", err
		}
		text, _ := optString(opts, "delay")
		var d time.Duration
		if t := strings.ToLower(strings.TrimSpace(text)); t != "off" && t != "0" && t != "none" {
			var err error
			// A bare number here means seconds, the unit Discord's own slowmode picker uses.
			if n, convErr := strconv.Atoi(t); convErr == nil {
				d = time.Duration(n) * time.Second
			} else if d, err = parseDuration(t); err != nil {
				return "", err
			}
		}
		if d > maxSlowmode {
			return "", userErrorf("Discord allows a slowmode of up to 6 hours.")
		}
		seconds := int(d / time.Second)
		if _, err := s.ChannelEdit(channelID, &discordgo.ChannelEdit{RateLimitPerUser: &seconds},
			auditLog(interactionUser(i), "/slowmode")); err != nil {
			return "", discordRefusal("change that channel (I need Manage Channels)", err)
		}
		if seconds == 0 {
			return fmt.Sprintf("Slowmode is off in <#%s>.", channelID), nil
		}
		return fmt.Sprintf("🐢 Members can post once every %s in <#%s>.", formatDuration(d), channelID), nil
	})
}

// lockBits are what /lock takes away from @everyone.
const lockBits = int64(discordgo.PermissionSendMessages | discordgo.PermissionSendMessagesInThreads |
	discordgo.PermissionCreatePublicThreads | discordgo.PermissionCreatePrivateThreads | discordgo.PermissionAddReactions)

// everyoneOverwrite is the channel's @everyone allow and deny bits.
func everyoneOverwrite(ch *discordgo.Channel, guildID string) (allow, deny int64) {
	for _, o := range ch.PermissionOverwrites {
		if o.ID == guildID && o.Type == discordgo.PermissionOverwriteTypeRole {
			return o.Allow, o.Deny
		}
	}
	return 0, 0
}

func setChannelLock(s *discordgo.Session, i *discordgo.InteractionCreate, opts cmdOpts, lock bool) (string, error) {
	channelID := targetChannelID(i, opts)
	if err := needChannelPermission(s, i, channelID, permManageChannels, "Manage Channels"); err != nil {
		return "", err
	}
	reason, _ := optString(opts, "reason")
	ch, err := s.Channel(channelID)
	if err != nil {
		return "", discordRefusal("see that channel", err)
	}
	allow, deny := everyoneOverwrite(ch, i.GuildID)
	locked := deny&discordgo.PermissionSendMessages != 0
	if lock == locked {
		if lock {
			return "", userErrorf("<#%s> is already locked.", channelID)
		}
		return "", userErrorf("<#%s> isn't locked.", channelID)
	}
	if lock {
		allow &^= lockBits
		deny |= lockBits
	} else {
		deny &^= lockBits
	}
	if err := s.ChannelPermissionSet(channelID, i.GuildID, discordgo.PermissionOverwriteTypeRole, allow, deny,
		auditLog(interactionUser(i), reason)); err != nil {
		return "", discordRefusal("change that channel's permissions (I need Manage Roles there)", err)
	}
	title, verb := "🔒 Channel locked", "locked"
	if !lock {
		title, verb = "🔓 Channel unlocked", "unlocked"
	}
	notice := &discordgo.MessageEmbed{Title: title, Color: colorNotice}
	if reason != "" {
		notice.Description = reason
	}
	if _, err := s.ChannelMessageSendEmbed(channelID, notice); err != nil {
		appLogger.Debugf("Could not post the lock notice in %s: %v", channelID, err)
	}
	postModLog(s, i.GuildID, &discordgo.MessageEmbed{
		Title:       title,
		Description: fmt.Sprintf("<#%s> %s by <@%s>.\n**Reason:** %s", channelID, verb, interactionUser(i).ID, reasonOrNone(reason)),
		Color:       colorNotice,
		Timestamp:   time.Now().Format(time.RFC3339),
	})
	return fmt.Sprintf("%s: <#%s>. Roles with their own Send Messages permission there can still post.", title, channelID), nil
}

func handleLockCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	startModeration(s, i, permManageChannels, "Manage Channels", func(opts cmdOpts) (string, error) {
		return setChannelLock(s, i, opts, true)
	})
}

func handleUnlockCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	startModeration(s, i, permManageChannels, "Manage Channels", func(opts cmdOpts) (string, error) {
		return setChannelLock(s, i, opts, false)
	})
}
