package main

import (
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/bwmarrin/discordgo"
)

// /announce: write a styled announcement in a pop-up and post it as the bot.
//
//	/announce send channel [ping] [ping_role] [publish]   opens the pop-up, then posts
//	/announce edit message                                opens the pop-up filled in with a posted announcement
//
// The pop-up has a title, the message, a color, an image link and a footer. Pings are
// sent as plain text above the embed so they notify people; pinging @everyone, @here or
// a role that isn't mentionable needs the member's own Mention Everyone permission. In
// an announcement channel, publish:True also sends it to servers that follow the channel.
//
// Everything the submit needs travels in the pop-up's custom ID, so nothing is kept in memory:
//
//	ann:<channelID>:<ping>:<publish 0|1>   ping is "", "everyone", "here" or a role ID
//	annedit:<channelID>:<messageID>

const (
	annTitleMax   = 256
	annBodyMax    = 4000
	annFooterMax  = 2048
	annDefaultHex = "5865F2"
)

func announceCommand() *SlashCommandHandler {
	ping := strOpt("ping", "Ping @everyone or @here with it", false, 0)
	ping.Choices = []*discordgo.ApplicationCommandOptionChoice{{Name: "@everyone", Value: "everyone"}, {Name: "@here", Value: "here"}}
	return &SlashCommandHandler{
		Name: "announce", Description: "Write and post an announcement, or edit one", Handler: handleAnnounceCommand,
		DefaultPermissions: &permManageGuild,
		Options: []*discordgo.ApplicationCommandOption{
			sub("send", "Write an announcement in a pop-up and post it",
				channelOpt("channel", "Where to post it", true, panelTextChannels...),
				ping,
				roleOpt("ping_role", "Ping a role with it", false),
				boolOpt("publish", "In an announcement channel, also send it to servers that follow it"),
			),
			sub("edit", "Change an announcement the bot posted",
				strOpt("message", "The announcement's message link (right-click it > Copy Message Link)", true, 200)),
		},
	}
}

func registerAnnounce() {
	registerGuildCommand(announceCommand())
	registerModalRoute("ann", handleAnnounceSubmit)
	registerModalRoute("annedit", handleAnnounceEditSubmit)
}

// ============================================================================
// RULES (no Discord calls, so they can be tested)
// ============================================================================

var messageLinkPattern = regexp.MustCompile(`discord(?:app)?\.com/channels/(\d+|@me)/(\d+)/(\d+)`)

// parseMessageLink reads a "Copy Message Link" URL.
func parseMessageLink(link string) (guildID, channelID, messageID string, ok bool) {
	m := messageLinkPattern.FindStringSubmatch(link)
	if m == nil {
		return "", "", "", false
	}
	return m[1], m[2], m[3], true
}

// parseColor reads "#ff8800", "ff8800", "0xFF8800" or a few color names. "" is the default color.
func parseColor(text string) (int, error) {
	t := strings.ToLower(strings.TrimSpace(text))
	names := map[string]string{
		"red": "ED4245", "green": "57F287", "blue": "3498DB", "blurple": "5865F2", "yellow": "FEE75C", "orange": "E67E22",
		"purple": "9B59B6", "pink": "EB459E", "white": "FFFFFF", "black": "23272A", "gray": "95A5A6", "grey": "95A5A6",
	}
	if t == "" {
		t = strings.ToLower(annDefaultHex)
	}
	if hex, ok := names[t]; ok {
		t = hex
	}
	t = strings.TrimPrefix(strings.TrimPrefix(t, "#"), "0x")
	bad := userErrorf("I couldn't read the color %q. Use a hex code like #FF8800, or a name like red or blue.", text)
	if len(t) != 6 {
		return 0, bad
	}
	v, err := strconv.ParseUint(t, 16, 32)
	if err != nil {
		return 0, bad
	}
	return int(v), nil
}

func checkImageURL(text string) (string, error) {
	t := strings.TrimSpace(text)
	if t == "" {
		return "", nil
	}
	u, err := url.Parse(t)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return "", userErrorf("The image must be a link starting with https://.")
	}
	return t, nil
}

type announcement struct {
	Title, Body, Footer, Image string
	Color                      int
}

func (a announcement) embed() *discordgo.MessageEmbed {
	e := &discordgo.MessageEmbed{Title: a.Title, Description: a.Body, Color: a.Color}
	if a.Footer != "" {
		e.Footer = &discordgo.MessageEmbedFooter{Text: a.Footer}
	}
	if a.Image != "" {
		e.Image = &discordgo.MessageEmbedImage{URL: a.Image}
	}
	return e
}

// readAnnouncement turns the pop-up's fields into an announcement.
func readAnnouncement(fields map[string]modalValue) (announcement, error) {
	a := announcement{
		Title:  strings.TrimSpace(fields["title"].text()),
		Body:   strings.TrimSpace(fields["body"].text()),
		Footer: strings.TrimSpace(fields["footer"].text()),
	}
	if a.Title == "" && a.Body == "" {
		return a, userErrorf("Write a title or a message.")
	}
	var err error
	if a.Color, err = parseColor(fields["color"].text()); err != nil {
		return a, err
	}
	if a.Image, err = checkImageURL(fields["image"].text()); err != nil {
		return a, err
	}
	return a, nil
}

// pingContent is the text above the embed and who it may notify.
func pingContent(ping string) (string, *discordgo.MessageAllowedMentions) {
	switch ping {
	case "":
		return "", &discordgo.MessageAllowedMentions{}
	case "everyone", "here":
		return "@" + ping, &discordgo.MessageAllowedMentions{Parse: []discordgo.AllowedMentionType{discordgo.AllowedMentionTypeEveryone}}
	default:
		return roleMention(ping), &discordgo.MessageAllowedMentions{Roles: []string{ping}}
	}
}

// ============================================================================
// POP-UP
// ============================================================================

func announcementModal(customID, heading string, a announcement) *discordgo.InteractionResponseData {
	optional := false
	color := ""
	if a.Color != 0 {
		color = fmt.Sprintf("#%06X", a.Color)
	}
	field := func(id, label, help string, style discordgo.TextInputStyle, maxLen int, value, placeholder string) discordgo.MessageComponent {
		return discordgo.Label{Label: label, Description: help, Component: discordgo.TextInput{
			CustomID: id, Style: style, Required: &optional, MaxLength: maxLen, Value: value, Placeholder: placeholder,
		}}
	}
	return &discordgo.InteractionResponseData{
		CustomID: customID,
		Title:    heading,
		Components: []discordgo.MessageComponent{
			field("title", "Title", "", discordgo.TextInputShort, annTitleMax, a.Title, "Version 1.2 is out!"),
			field("body", "Message", "Markdown works: **bold**, lists, links", discordgo.TextInputParagraph, annBodyMax, a.Body, ""),
			field("color", "Color", "A hex code like #FF8800, or a name like red or blue", discordgo.TextInputShort, 20, color, "#"+annDefaultHex),
			field("image", "Image link", "Shown large under the message", discordgo.TextInputShort, 500, a.Image, "https://…"),
			field("footer", "Footer", "Small text at the bottom", discordgo.TextInputShort, annFooterMax, a.Footer, ""),
		},
	}
}

func handleAnnounceCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if i.GuildID == "" || !canManageServer(i) {
		respondEphemeral(s, i, "You need the **Manage Server** permission for this.")
		return
	}
	sub, opts := commandOptions(i)
	var data *discordgo.InteractionResponseData
	var err error
	if sub == "send" {
		data, err = announceSendModal(s, i, opts)
	} else {
		data, err = announceEditModal(s, i, opts)
	}
	if err != nil {
		respondEphemeral(s, i, errorText("/announce "+sub, err))
		return
	}
	if err := showModal(s, i, data); err != nil {
		appLogger.Warnf("Could not open the /announce pop-up: %v", err)
	}
}

func canMentionEveryone(i *discordgo.InteractionCreate) bool {
	return i.Member != nil && i.Member.Permissions&(discordgo.PermissionMentionEveryone|discordgo.PermissionAdministrator) != 0
}

func announceSendModal(s *discordgo.Session, i *discordgo.InteractionCreate, opts cmdOpts) (*discordgo.InteractionResponseData, error) {
	ch := optChannel(i, opts, "channel")
	if err := checkPanelChannel(s, ch.ID); err != nil {
		return nil, err
	}
	ping, _ := optString(opts, "ping")
	if role := optRole(i, opts, "ping_role"); role != nil {
		if ping != "" {
			return nil, userErrorf("Pick either @everyone/@here or a role to ping, not both.")
		}
		if role.ID == i.GuildID {
			ping = "everyone"
		} else {
			if !role.Mentionable && !canMentionEveryone(i) {
				return nil, userErrorf("%s can't be mentioned by everyone, and you don't have the Mention Everyone permission.", roleMention(role.ID))
			}
			ping = role.ID
		}
	}
	if (ping == "everyone" || ping == "here") && !canMentionEveryone(i) {
		return nil, userErrorf("Pinging @everyone or @here needs the Mention Everyone permission.")
	}
	publish := "0"
	if p, _ := optBool(opts, "publish"); p {
		if ch.Type != discordgo.ChannelTypeGuildNews {
			return nil, userErrorf("Only announcement channels can publish to following servers. Leave `publish` off, or pick an announcement channel.")
		}
		publish = "1"
	}
	return announcementModal(fmt.Sprintf("ann:%s:%s:%s", ch.ID, ping, publish), "New announcement", announcement{}), nil
}

// botAnnouncement fetches a message the bot posted with an embed, from its link.
func botAnnouncement(s *discordgo.Session, guildID, link string) (*discordgo.Message, error) {
	g, channelID, messageID, ok := parseMessageLink(link)
	if !ok {
		return nil, userErrorf("That isn't a message link. Right-click the announcement and pick Copy Message Link.")
	}
	if g != guildID {
		return nil, userErrorf("That message is in another server.")
	}
	msg, err := s.ChannelMessage(channelID, messageID)
	if err != nil {
		if isNotFound(err) {
			return nil, userErrorf("I couldn't find that message. It may have been deleted.")
		}
		return nil, discordRefusal("read that message", err)
	}
	if msg.Author == nil || msg.Author.ID != s.State.User.ID || len(msg.Embeds) == 0 {
		return nil, userErrorf("I can only edit announcements I posted.")
	}
	return msg, nil
}

func announceEditModal(s *discordgo.Session, i *discordgo.InteractionCreate, opts cmdOpts) (*discordgo.InteractionResponseData, error) {
	link, _ := optString(opts, "message")
	msg, err := botAnnouncement(s, i.GuildID, link)
	if err != nil {
		return nil, err
	}
	e := msg.Embeds[0]
	a := announcement{Title: e.Title, Body: e.Description, Color: e.Color}
	if e.Footer != nil {
		a.Footer = e.Footer.Text
	}
	if e.Image != nil {
		a.Image = e.Image.URL
	}
	return announcementModal(fmt.Sprintf("annedit:%s:%s", msg.ChannelID, msg.ID), "Edit announcement", a), nil
}

// inThisServer reports whether channelID belongs to the server the interaction came from.
func inThisServer(s *discordgo.Session, i *discordgo.InteractionCreate, channelID string) bool {
	ch, err := s.State.Channel(channelID)
	if err != nil {
		if ch, err = s.Channel(channelID); err != nil {
			return false
		}
	}
	return ch.GuildID == i.GuildID
}

func handleAnnounceSubmit(s *discordgo.Session, i *discordgo.InteractionCreate, customID string) {
	parts := strings.Split(customID, ":")
	if len(parts) != 4 {
		respondEphemeral(s, i, "That pop-up has expired. Please try again.")
		return
	}
	if !canManageServer(i) {
		respondEphemeral(s, i, "You need the **Manage Server** permission for this.")
		return
	}
	channelID, ping, publish := parts[1], parts[2], parts[3] == "1"
	// Checked again here as well as in /announce: never trust a custom ID alone.
	if (ping == "everyone" || ping == "here") && !canMentionEveryone(i) {
		respondEphemeral(s, i, "Pinging @everyone or @here needs the Mention Everyone permission.")
		return
	}
	if !inThisServer(s, i, channelID) {
		respondEphemeral(s, i, "I can't post in that channel.")
		return
	}
	a, err := readAnnouncement(modalValues(i))
	if err != nil {
		respondEphemeral(s, i, errorText("/announce", err))
		return
	}
	if err := deferEphemeral(s, i); err != nil {
		appLogger.Warnf("Could not defer an announcement: %v", err)
		return
	}
	content, mentions := pingContent(ping)
	msg, err := s.ChannelMessageSendComplex(channelID, &discordgo.MessageSend{
		Content: content, Embeds: []*discordgo.MessageEmbed{a.embed()}, AllowedMentions: mentions,
	})
	if err != nil {
		editResponse(s, i, errorText("Posting an announcement", discordRefusal("post there", err)))
		return
	}
	reply := fmt.Sprintf("📣 Posted: %s\nChange it any time with `/announce edit`.", messageLink(i.GuildID, channelID, msg.ID))
	if publish {
		if _, err := s.ChannelMessageCrosspost(channelID, msg.ID); err != nil {
			appLogger.Warnf("Could not publish announcement %s: %v", msg.ID, err)
			reply += "\n⚠️ I couldn't publish it to following servers. Use the Publish button on the message."
		} else {
			reply += "\nPublished to following servers."
		}
	}
	editResponse(s, i, reply)
}

func handleAnnounceEditSubmit(s *discordgo.Session, i *discordgo.InteractionCreate, customID string) {
	parts := strings.Split(customID, ":")
	if len(parts) != 3 {
		respondEphemeral(s, i, "That pop-up has expired. Please try again.")
		return
	}
	if !canManageServer(i) {
		respondEphemeral(s, i, "You need the **Manage Server** permission for this.")
		return
	}
	if !inThisServer(s, i, parts[1]) {
		respondEphemeral(s, i, "I can't edit messages in that channel.")
		return
	}
	a, err := readAnnouncement(modalValues(i))
	if err != nil {
		respondEphemeral(s, i, errorText("/announce edit", err))
		return
	}
	embeds := []*discordgo.MessageEmbed{a.embed()}
	_, err = s.ChannelMessageEditComplex(&discordgo.MessageEdit{Channel: parts[1], ID: parts[2], Embeds: &embeds})
	if err != nil {
		respondEphemeral(s, i, errorText("Editing an announcement", discordRefusal("edit it", err)))
		return
	}
	respondEphemeral(s, i, "✏️ Updated: "+messageLink(i.GuildID, parts[1], parts[2]))
}
