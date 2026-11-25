package main

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

// Gateway events behind /settings: welcome and goodbye messages, auto-roles, the member
// log and the message log.
//
// Joins and leaves need the Server Members intent, and message text needs the Message
// Content intent. Both are "privileged": Discord refuses the whole connection when the
// bot asks for one that isn't switched on in the Developer Portal. So the bot only asks
// when its host says so with DISCORD_MEMBERS_INTENT=true and
// DISCORD_MESSAGE_CONTENT_INTENT=true.

const newAccountAge = 7 * 24 * time.Hour

func envFlag(name string) bool {
	on, _ := strconv.ParseBool(strings.TrimSpace(os.Getenv(name)))
	return on
}

func memberEventsEnabled() bool   { return envFlag("DISCORD_MEMBERS_INTENT") }
func messageContentEnabled() bool { return envFlag("DISCORD_MESSAGE_CONTENT_INTENT") }

// botIntents is every gateway intent the bot asks for.
func botIntents() discordgo.Intent {
	intents := discordgo.IntentsGuildMessages | discordgo.IntentsGuilds | discordgo.IntentsDirectMessages
	if memberEventsEnabled() {
		intents |= discordgo.IntentsGuildMembers
	}
	if messageContentEnabled() {
		intents |= discordgo.IntentMessageContent
	}
	return intents
}

// serverEventHandlers are added to every shard.
func serverEventHandlers() []interface{} {
	return []interface{}{
		handleMemberJoin, handleMemberUpdate, handleMemberLeave,
		handleMessageCreateForLog, handleMessageUpdateForLog, handleMessageDeleteForLog, handleMessageBulkDeleteForLog,
	}
}

// ============================================================================
// MEMBERS
// ============================================================================

func accountCreated(userID string) time.Time {
	t, err := discordgo.SnowflakeTimestamp(userID)
	if err != nil {
		return time.Time{}
	}
	return t
}

func memberJoinEmbed(u *discordgo.User, count int, now time.Time) *discordgo.MessageEmbed {
	created := accountCreated(u.ID)
	desc := fmt.Sprintf("<@%s> (`%s`)\nAccount made <t:%d:R>", u.ID, u.ID, created.Unix())
	if !created.IsZero() && now.Sub(created) < newAccountAge {
		desc += "\n⚠️ **New account**"
	}
	embed := &discordgo.MessageEmbed{
		Title: "📥 Member joined", Description: desc, Color: colorOpened,
		Thumbnail: &discordgo.MessageEmbedThumbnail{URL: u.AvatarURL("128")},
		Timestamp: now.Format(time.RFC3339),
	}
	if count > 0 {
		embed.Footer = &discordgo.MessageEmbedFooter{Text: fmt.Sprintf("Member #%d · %s", count, u.Username)}
	}
	return embed
}

func handleMemberJoin(s *discordgo.Session, m *discordgo.GuildMemberAdd) {
	if m.Member == nil || m.User == nil {
		return
	}
	g := serverSettings(m.GuildID)
	if g == nil {
		return
	}
	count := guildMemberCount(s, m.GuildID)
	if g.MemberLogChannel != "" {
		postToLogChannel(s, m.GuildID, func(g *guildSettings) string { return g.MemberLogChannel }, memberJoinEmbed(m.User, count, time.Now()))
	}
	if m.User.Bot {
		return
	}
	if g.WelcomeChannel != "" {
		text := renderGreeting(orDefault(g.WelcomeMessage, defaultWelcomeMessage), m.User, guildName(s, m.GuildID), count)
		if err := sendGreeting(s, g.WelcomeChannel, text, m.User.ID); err != nil {
			appLogger.Warnf("Could not post the welcome message in %s: %v", m.GuildID, err)
		}
	}
	// Members who still have to accept the server's rules get their roles once they do.
	if !m.Pending {
		giveAutoRoles(s, m.GuildID, m.User.ID, g.AutoRoles)
	}
}

func handleMemberUpdate(s *discordgo.Session, m *discordgo.GuildMemberUpdate) {
	if m.Member == nil || m.User == nil || m.User.Bot || m.BeforeUpdate == nil {
		return
	}
	if m.BeforeUpdate.Pending && !m.Pending {
		if g := serverSettings(m.GuildID); g != nil {
			giveAutoRoles(s, m.GuildID, m.User.ID, g.AutoRoles)
		}
	}
}

// giveAutoRoles hands out the auto-roles, skipping any that have since become unsafe to give.
func giveAutoRoles(s *discordgo.Session, guildID, userID string, roleIDs []string) {
	if len(roleIDs) == 0 {
		return
	}
	facts, err := loadRoleFacts(s, guildID)
	if err != nil {
		appLogger.Warnf("Auto-roles in %s: %v", guildID, err)
		return
	}
	for _, id := range roleIDs {
		role, ok := facts.Roles[id]
		if !ok {
			continue // deleted
		}
		if why := roleProblem(role, facts); why != "" {
			appLogger.Warnf("Skipping auto-role %s in %s: %s", id, guildID, why)
			continue
		}
		if err := s.GuildMemberRoleAdd(guildID, userID, id, discordgo.WithAuditLogReason(url.PathEscape("Auto-role"))); err != nil {
			appLogger.Warnf("Could not give auto-role %s in %s: %v", id, guildID, err)
		}
	}
}

func handleMemberLeave(s *discordgo.Session, m *discordgo.GuildMemberRemove) {
	if m.Member == nil || m.User == nil {
		return
	}
	g := serverSettings(m.GuildID)
	if g == nil {
		return
	}
	if g.MemberLogChannel != "" {
		desc := fmt.Sprintf("**%s** <@%s> (`%s`)", m.User.Username, m.User.ID, m.User.ID)
		if m.JoinedAt.Unix() > 0 {
			desc += fmt.Sprintf("\nJoined <t:%d:R>", m.JoinedAt.Unix())
		}
		if len(m.Roles) > 0 {
			desc += "\nRoles: " + mentionAll(m.Roles)
		}
		postToLogChannel(s, m.GuildID, func(g *guildSettings) string { return g.MemberLogChannel }, &discordgo.MessageEmbed{
			Title: "📤 Member left", Description: truncateRunes(desc, 4000), Color: colorClosed,
			Thumbnail: &discordgo.MessageEmbedThumbnail{URL: m.User.AvatarURL("128")},
			Timestamp: time.Now().Format(time.RFC3339),
		})
	}
	if g.GoodbyeChannel != "" && !m.User.Bot {
		text := renderGreeting(orDefault(g.GoodbyeMessage, defaultGoodbyeMessage), m.User, guildName(s, m.GuildID), guildMemberCount(s, m.GuildID))
		// No ping: they can't see the server any more.
		_, err := s.ChannelMessageSendComplex(g.GoodbyeChannel, &discordgo.MessageSend{
			Content: truncateRunes(text, 2000), AllowedMentions: &discordgo.MessageAllowedMentions{},
		})
		if err != nil {
			appLogger.Warnf("Could not post the goodbye message in %s: %v", m.GuildID, err)
		}
	}
}

// ============================================================================
// MESSAGE LOG
// ============================================================================

// Discord's delete event carries only the message's ID, so the bot remembers recent
// messages in servers that have a message log, to show what was deleted or edited.

const messageCacheSize = 20000

type cachedMessage struct {
	ID          string
	GuildID     string
	ChannelID   string
	AuthorID    string
	AuthorName  string
	Content     string
	Attachments []string
	At          time.Time
}

// messageCache keeps the newest messageCacheSize messages.
type messageCache struct {
	mu    sync.Mutex
	size  int
	byID  map[string]*cachedMessage
	order []string // ring of IDs, oldest overwritten first
	next  int
}

func newMessageCache(size int) *messageCache {
	return &messageCache{size: size, byID: map[string]*cachedMessage{}, order: make([]string, size)}
}

func (c *messageCache) put(m *cachedMessage) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.byID[m.ID]; ok {
		c.byID[m.ID] = m
		return
	}
	if old := c.order[c.next]; old != "" {
		delete(c.byID, old)
	}
	c.order[c.next] = m.ID
	c.next = (c.next + 1) % c.size
	c.byID[m.ID] = m
}

func (c *messageCache) get(id string) *cachedMessage {
	c.mu.Lock()
	defer c.mu.Unlock()
	if m, ok := c.byID[id]; ok {
		copied := *m
		return &copied
	}
	return nil
}

// take returns and forgets a message.
func (c *messageCache) take(id string) *cachedMessage {
	c.mu.Lock()
	defer c.mu.Unlock()
	m, ok := c.byID[id]
	if !ok {
		return nil
	}
	delete(c.byID, id) // its slot in order is reused later; put skips IDs no longer present
	return m
}

var recentMessages = newMessageCache(messageCacheSize)

// messageLogChannel is where to log a change in channelID, or "" to log nothing (no log,
// or the change is in the log channel itself).
func messageLogChannel(guildID, channelID string) string {
	if guildID == "" {
		return ""
	}
	g := serverSettings(guildID)
	if g == nil || g.MessageLogChannel == "" || g.MessageLogChannel == channelID {
		return ""
	}
	return g.MessageLogChannel
}

func handleMessageCreateForLog(s *discordgo.Session, m *discordgo.MessageCreate) {
	if m.Author == nil || m.Author.Bot || messageLogChannel(m.GuildID, m.ChannelID) == "" {
		return
	}
	files := make([]string, 0, len(m.Attachments))
	for _, a := range m.Attachments {
		files = append(files, a.Filename)
	}
	recentMessages.put(&cachedMessage{
		ID: m.ID, GuildID: m.GuildID, ChannelID: m.ChannelID, AuthorID: m.Author.ID, AuthorName: m.Author.Username,
		Content: m.Content, Attachments: files, At: m.Timestamp,
	})
}

func quoteContent(text string) string {
	if strings.TrimSpace(text) == "" {
		return "*(no text)*"
	}
	return truncateRunes(text, 1000)
}

func deletedMessageEmbed(m *cachedMessage) *discordgo.MessageEmbed {
	embed := &discordgo.MessageEmbed{
		Title:       "🗑️ Message deleted",
		Description: fmt.Sprintf("By <@%s> in <#%s>, sent <t:%d:R>", m.AuthorID, m.ChannelID, m.At.Unix()),
		Color:       colorClosed,
		Fields:      []*discordgo.MessageEmbedField{{Name: "Message", Value: quoteContent(m.Content)}},
		Footer:      &discordgo.MessageEmbedFooter{Text: m.AuthorName + " · " + m.AuthorID},
		Timestamp:   time.Now().Format(time.RFC3339),
	}
	if len(m.Attachments) > 0 {
		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{Name: "Attachments", Value: truncateRunes(strings.Join(m.Attachments, "\n"), 1000)})
	}
	return embed
}

func editedMessageEmbed(before *cachedMessage, after string, link string) *discordgo.MessageEmbed {
	return &discordgo.MessageEmbed{
		Title:       "✏️ Message edited",
		Description: fmt.Sprintf("By <@%s> in <#%s> · [Jump to message](%s)", before.AuthorID, before.ChannelID, link),
		Color:       colorNotice,
		Fields: []*discordgo.MessageEmbedField{
			{Name: "Before", Value: quoteContent(before.Content)},
			{Name: "After", Value: quoteContent(after)},
		},
		Footer:    &discordgo.MessageEmbedFooter{Text: before.AuthorName + " · " + before.AuthorID},
		Timestamp: time.Now().Format(time.RFC3339),
	}
}

func sendMessageLog(s *discordgo.Session, guildID, logChannel string, embed *discordgo.MessageEmbed) {
	_, err := s.ChannelMessageSendComplex(logChannel, &discordgo.MessageSend{
		Embeds: []*discordgo.MessageEmbed{embed}, AllowedMentions: &discordgo.MessageAllowedMentions{},
	})
	if err != nil {
		appLogger.Warnf("Could not post to the message log in %s: %v", guildID, err)
	}
}

func handleMessageUpdateForLog(s *discordgo.Session, m *discordgo.MessageUpdate) {
	if m.Message == nil {
		return
	}
	logChannel := messageLogChannel(m.GuildID, m.ChannelID)
	if logChannel == "" {
		return
	}
	before := recentMessages.get(m.ID)
	// Link previews loading also count as edits; only a change of text is logged.
	if before == nil || before.Content == m.Content {
		return
	}
	sendMessageLog(s, m.GuildID, logChannel, editedMessageEmbed(before, m.Content, messageLink(m.GuildID, m.ChannelID, m.ID)))
	before.Content = m.Content
	recentMessages.put(before)
}

func handleMessageDeleteForLog(s *discordgo.Session, m *discordgo.MessageDelete) {
	if m.Message == nil {
		return
	}
	logChannel := messageLogChannel(m.GuildID, m.ChannelID)
	if logChannel == "" {
		return
	}
	if cached := recentMessages.take(m.ID); cached != nil {
		sendMessageLog(s, m.GuildID, logChannel, deletedMessageEmbed(cached))
	}
}

func handleMessageBulkDeleteForLog(s *discordgo.Session, m *discordgo.MessageDeleteBulk) {
	logChannel := messageLogChannel(m.GuildID, m.ChannelID)
	if logChannel == "" {
		return
	}
	var b strings.Builder
	known := 0
	for _, id := range m.Messages {
		if c := recentMessages.take(id); c != nil {
			known++
			line := fmt.Sprintf("**%s**: %s\n", c.AuthorName, truncateRunes(strings.ReplaceAll(c.Content, "\n", " "), 150))
			if b.Len()+len(line) < 3500 {
				b.WriteString(line)
			}
		}
	}
	desc := fmt.Sprintf("%d message%s deleted in <#%s>.", len(m.Messages), plural(len(m.Messages)), m.ChannelID)
	if known > 0 {
		desc += "\n\n" + b.String()
	}
	sendMessageLog(s, m.GuildID, logChannel, &discordgo.MessageEmbed{
		Title: "🧹 Messages bulk deleted", Description: truncateRunes(desc, 4000), Color: colorClosed,
		Timestamp: time.Now().Format(time.RFC3339),
	})
}
