package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

// Opening tickets: a type's button opens its intake pop-ups (if it has questions), then
// makes the ticket channel. A pop-up can't open another pop-up, so longer intakes have a
// Continue button between pages; the answers so far are kept in memory for formDraftTTL.
//
// Custom IDs (never change a format already posted on a message):
//
//	ticket:<type id>:open   a type's Open button (on /tickettype post and /ticket panel)
//	tickets:mine            🔎 My tickets on a panel
//	tkttry:<type id>        👀 Try the questions, from /tickettype show (nothing is saved)
//	tktpage:<token>         one intake page's pop-up
//	tktnext:<token>         Continue to the next intake page

const formDraftTTL = 15 * time.Minute

// chatTalk is what the member, the staff roles and the bot may do in a ticket.
const chatTalk = discordgo.PermissionViewChannel | discordgo.PermissionSendMessages | discordgo.PermissionReadMessageHistory |
	discordgo.PermissionAttachFiles | discordgo.PermissionEmbedLinks

func openButton(tt ticketType) discordgo.Button {
	b := discordgo.Button{Label: truncateRunes(tt.Name, 80), Style: discordgo.PrimaryButton, CustomID: fmt.Sprintf("ticket:%d:open", tt.ID)}
	if e, err := parseButtonEmoji(tt.Emoji); err == nil && e != nil {
		b.Emoji = e
	}
	return b
}

func myTicketsButton() discordgo.Button {
	return discordgo.Button{Label: "🔎 My tickets", Style: discordgo.SecondaryButton, CustomID: "tickets:mine"}
}

// ticketPanelButtons is one Open button per type, then 🔎 My tickets, five to a row.
func ticketPanelButtons(types []ticketType) []discordgo.MessageComponent {
	var buttons []discordgo.MessageComponent
	for _, tt := range types {
		buttons = append(buttons, openButton(tt))
	}
	buttons = append(buttons, myTicketsButton())
	var rows []discordgo.MessageComponent
	for len(buttons) > 0 {
		n := min(5, len(buttons))
		rows = append(rows, discordgo.ActionsRow{Components: buttons[:n]})
		buttons = buttons[n:]
	}
	return rows
}

// ============================================================================
// INTAKE DRAFTS AND POP-UPS
// ============================================================================

type draftSlot struct {
	draft   *formDraft
	expires time.Time
}

var (
	draftsMu sync.Mutex
	drafts   = map[string]*draftSlot{}
)

func newToken() string {
	b := make([]byte, 9)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func saveDraft(d *formDraft) string {
	draftsMu.Lock()
	defer draftsMu.Unlock()
	now := time.Now()
	for token, slot := range drafts {
		if now.After(slot.expires) {
			delete(drafts, token)
		}
	}
	token := newToken()
	drafts[token] = &draftSlot{draft: d, expires: now.Add(formDraftTTL)}
	return token
}

// takeDraft returns the draft for token if it's still fresh and belongs to userID.
func takeDraft(token, userID string) *formDraft {
	draftsMu.Lock()
	defer draftsMu.Unlock()
	slot, ok := drafts[token]
	if !ok || time.Now().After(slot.expires) || slot.draft.UserID != userID {
		return nil
	}
	slot.expires = time.Now().Add(formDraftTTL)
	return slot.draft
}

func dropDraft(token string) {
	draftsMu.Lock()
	defer draftsMu.Unlock()
	delete(drafts, token)
}

func questionField(q formQuestion) string { return "q" + strconv.FormatInt(q.ID, 10) }

// buildIntakePage is the pop-up for the draft's current page.
func buildIntakePage(d *formDraft, token string) *discordgo.InteractionResponseData {
	page := ""
	if d.Total() > 1 {
		page = fmt.Sprintf(" (%d/%d)", d.Page+1, d.Total())
	}
	title := truncateRunes(d.FormName, formNameMax-len([]rune(page))) + page
	if strings.TrimSpace(title) == "" {
		title = "New ticket"
	}
	var components []discordgo.MessageComponent
	if d.Header != "" && d.Page == 0 {
		components = append(components, discordgo.TextDisplay{Content: truncateRunes(d.Header, formHeaderMax)})
	}
	for _, q := range d.Current() {
		required := q.Required
		var field discordgo.MessageComponent
		if q.Kind == kindChoice || q.Kind == kindMulti {
			options := make([]discordgo.SelectMenuOption, len(q.Choices))
			for n, c := range q.Choices {
				options[n] = discordgo.SelectMenuOption{Label: truncateRunes(c, 100), Value: strconv.Itoa(n)}
			}
			least, most, placeholder := 1, 1, "Pick one"
			if !q.Required {
				least = 0
			}
			if q.Kind == kindMulti {
				most, placeholder = len(q.Choices), "Pick any that apply"
			}
			field = discordgo.SelectMenu{
				MenuType: discordgo.StringSelectMenu, CustomID: questionField(q), Placeholder: placeholder,
				MinValues: &least, MaxValues: most, Options: options, Required: &required,
			}
		} else {
			style := discordgo.TextInputShort
			if q.Kind == kindParagraph {
				style = discordgo.TextInputParagraph
			}
			field = discordgo.TextInput{CustomID: questionField(q), Style: style, Required: &required, MaxLength: q.Limit()}
		}
		components = append(components, discordgo.Label{Label: truncateRunes(q.Label, formLabelMax), Description: q.Help, Component: field})
	}
	return &discordgo.InteractionResponseData{CustomID: "tktpage:" + token, Title: title, Components: components}
}

// modalValue is what a member entered in one pop-up field: typed text, or the picked option values.
type modalValue struct{ values []string }

func (v modalValue) text() string { return strings.Join(v.values, "") }

// modalValues reads a submitted pop-up's fields by custom ID.
func modalValues(i *discordgo.InteractionCreate) map[string]modalValue {
	out := map[string]modalValue{}
	var walk func(c discordgo.MessageComponent)
	walk = func(c discordgo.MessageComponent) {
		switch v := c.(type) {
		case *discordgo.Label:
			walk(v.Component)
		case discordgo.Label:
			walk(v.Component)
		case *discordgo.ActionsRow:
			for _, inner := range v.Components {
				walk(inner)
			}
		case discordgo.ActionsRow:
			for _, inner := range v.Components {
				walk(inner)
			}
		case *discordgo.TextInput:
			out[v.CustomID] = modalValue{values: []string{v.Value}}
		case *discordgo.SelectMenu:
			out[v.CustomID] = modalValue{values: v.Values}
		}
	}
	for _, c := range i.ModalSubmitData().Components {
		walk(c)
	}
	return out
}

// pageValues turns a page's fields into answers by question: typed text, or the chosen choices' words.
func pageValues(questions []formQuestion, fields map[string]modalValue) map[int64][]string {
	out := map[int64][]string{}
	for _, q := range questions {
		got := fields[questionField(q)]
		if q.Kind == kindChoice || q.Kind == kindMulti {
			var picked []string
			for _, v := range got.values {
				if n, err := strconv.Atoi(v); err == nil && n >= 0 && n < len(q.Choices) {
					picked = append(picked, q.Choices[n])
				}
			}
			out[q.ID] = picked
		} else {
			out[q.ID] = []string{got.text()}
		}
	}
	return out
}

// ============================================================================
// BUTTONS
// ============================================================================

func handleOpenButton(s *discordgo.Session, i *discordgo.InteractionCreate, customID string) {
	parts := strings.Split(customID, ":")
	id, ok := parseID(parts, 1)
	if !ok || len(parts) != 3 || parts[2] != "open" {
		respondEphemeral(s, i, "This button no longer does anything.")
		return
	}
	if ticketsOff(s, i) {
		return
	}
	if err := startTicket(s, i, id, false); err != nil {
		replyEphemeral(s, i, errorText("Opening a ticket", err))
	}
}

func handleTryButton(s *discordgo.Session, i *discordgo.InteractionCreate, customID string) {
	id, ok := parseID(strings.Split(customID, ":"), 1)
	if !ok {
		respondEphemeral(s, i, "This button no longer does anything.")
		return
	}
	if ticketsOff(s, i) {
		return
	}
	if err := startTicket(s, i, id, true); err != nil {
		replyEphemeral(s, i, errorText("Trying a ticket type", err))
	}
}

// canOpen is why this member can't open a ticket of this type now, if they can't.
func canOpen(tt *ticketType, userID string) error {
	if !tt.Open {
		return userErrorf("**%s** isn't taking new tickets right now.", tt.Name)
	}
	open, err := tickets.OpenTicketsOf(tt.ID, userID)
	if err != nil {
		return err
	}
	if len(open) >= max(1, tt.MaxOpen) {
		var where []string
		for _, t := range open {
			if t.ChannelID != "" {
				where = append(where, "<#"+t.ChannelID+">")
			}
		}
		return userErrorf("You already have an open **%s** ticket: %s. Please use that one, or close it first.", tt.Name, strings.Join(where, ", "))
	}
	return nil
}

// startTicket shows the intake questions, or opens the ticket straight away when there are none.
// preview: staff trying the questions; nothing is saved or opened.
func startTicket(s *discordgo.Session, i *discordgo.InteractionCreate, typeID int64, preview bool) error {
	tt, err := needType(i.GuildID, typeID)
	if err != nil {
		return err
	}
	user := interactionUser(i)
	if !preview {
		if err := canOpen(tt, user.ID); err != nil {
			return err
		}
	}
	questions, err := tickets.Questions(typeID)
	if err != nil {
		return err
	}
	if len(questions) == 0 {
		if preview {
			respondEphemeral(s, i, "👀 This type has no questions: pressing its button opens a ticket straight away.")
			return nil
		}
		if err := deferEphemeral(s, i); err != nil {
			return err
		}
		finishOpening(s, i, tt, nil)
		return nil
	}
	d := &formDraft{FormID: typeID, GuildID: i.GuildID, UserID: user.ID, FormName: tt.Name, Questions: questions, Header: tt.Header, Preview: preview}
	token := saveDraft(d)
	if err := showModal(s, i, buildIntakePage(d, token)); err != nil {
		dropDraft(token)
		return err
	}
	return nil
}

func handleNextPageButton(s *discordgo.Session, i *discordgo.InteractionCreate, customID string) {
	token := strings.TrimPrefix(customID, "tktnext:")
	d := takeDraft(token, interactionUser(i).ID)
	if d == nil || d.Done() {
		respondEphemeral(s, i, fmt.Sprintf("These questions have expired (you have %d minutes between pages). Please press the button again.", int(formDraftTTL.Minutes())))
		return
	}
	if err := showModal(s, i, buildIntakePage(d, token)); err != nil {
		appLogger.Warnf("Could not open the next intake page: %v", err)
	}
}

func handleIntakePageSubmit(s *discordgo.Session, i *discordgo.InteractionCreate, customID string) {
	token := strings.TrimPrefix(customID, "tktpage:")
	d := takeDraft(token, interactionUser(i).ID)
	if d == nil || d.Done() {
		respondEphemeral(s, i, "These questions have expired. Please press the button again.")
		return
	}
	if ticketsOff(s, i) {
		return
	}
	d.Record(pageValues(d.Current(), modalValues(i)))
	if !d.Done() {
		err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: fmt.Sprintf("Page %d of %d saved. Press **Continue** for the next page (within %d minutes).", d.Page, d.Total(), int(formDraftTTL.Minutes())),
				Flags:   discordgo.MessageFlagsEphemeral,
				Components: []discordgo.MessageComponent{discordgo.ActionsRow{Components: []discordgo.MessageComponent{
					discordgo.Button{Label: fmt.Sprintf("Continue to page %d of %d ▶", d.Page+1, d.Total()), Style: discordgo.PrimaryButton, CustomID: "tktnext:" + token},
				}}},
			},
		})
		if err != nil {
			appLogger.Warnf("Could not answer an intake page: %v", err)
		}
		return
	}
	dropDraft(token)
	if d.Preview {
		text := formTranscript("Preview of "+d.FormName, "Nothing was saved or opened.", d.Saved())
		respondEphemeral(s, i, "👀 That's how the questions work. Nothing was opened.\n```\n"+truncateRunes(strings.ReplaceAll(text, "```", "'''"), 1800)+"\n```")
		return
	}
	tt, err := needType(d.GuildID, d.FormID)
	if err != nil {
		respondEphemeral(s, i, errorText("Opening a ticket", err))
		return
	}
	if err := deferEphemeral(s, i); err != nil {
		appLogger.Warnf("Could not defer an intake submit: %v", err)
		return
	}
	finishOpening(s, i, tt, d.Saved())
}

// finishOpening opens the ticket and tells the member where it is. The interaction is already deferred.
func finishOpening(s *discordgo.Session, i *discordgo.InteractionCreate, tt *ticketType, answers []formAnswer) {
	channelID, err := openTicket(s, tt, i.Member, answers)
	if err != nil {
		editResponse(s, i, errorText("Opening a ticket", err))
		return
	}
	editResponse(s, i, fmt.Sprintf("✅ Your ticket is open: <#%s>. Staff will be with you there.", channelID))
}

// ============================================================================
// OPENING
// ============================================================================

func guildChannels(s *discordgo.Session, guildID string) []*discordgo.Channel {
	if g, err := s.State.Guild(guildID); err == nil && len(g.Channels) > 0 {
		return g.Channels
	}
	channels, err := s.GuildChannels(guildID)
	if err != nil {
		return nil
	}
	return channels
}

func categoryChannelCount(s *discordgo.Session, guildID, categoryID string) int {
	n := 0
	for _, ch := range guildChannels(s, guildID) {
		if ch.ParentID == categoryID {
			n++
		}
	}
	return n
}

// ticketOverwrites: hidden from @everyone; the bot, the member and the staff roles may talk.
func ticketOverwrites(guildID, botID, userID string, staff []string) []*discordgo.PermissionOverwrite {
	out := []*discordgo.PermissionOverwrite{
		{ID: guildID, Type: discordgo.PermissionOverwriteTypeRole, Deny: discordgo.PermissionViewChannel},
		{ID: botID, Type: discordgo.PermissionOverwriteTypeMember, Allow: chatTalk | discordgo.PermissionManageChannels},
		{ID: userID, Type: discordgo.PermissionOverwriteTypeMember, Allow: chatTalk},
	}
	for _, id := range staff {
		out = append(out, &discordgo.PermissionOverwrite{ID: id, Type: discordgo.PermissionOverwriteTypeRole, Allow: chatTalk})
	}
	return out
}

// openTicket makes the ticket and its channel, posts the opening message and logs it. It
// checks again that the member may open one (the questions took a while).
func openTicket(s *discordgo.Session, tt *ticketType, member *discordgo.Member, answers []formAnswer) (string, error) {
	if member == nil || member.User == nil {
		return "", userErrorf("I couldn't tell who you are. Please try again.")
	}
	fresh, err := needType(tt.GuildID, tt.ID)
	if err != nil {
		return "", err
	}
	tt = fresh
	if err := canOpen(tt, member.User.ID); err != nil {
		return "", err
	}
	staff, err := ticketStaffRoles(s, tt)
	if err != nil {
		return "", err
	}
	if len(staff) == 0 {
		return "", userErrorf("**%s** has no staff to answer tickets yet. Please let an admin know.", tt.Name)
	}
	channels := guildChannels(s, tt.GuildID)
	categoryFound, inCategory := false, 0
	for _, ch := range channels {
		categoryFound = categoryFound || ch.ID == tt.CategoryID
		if ch.ParentID == tt.CategoryID {
			inCategory++
		}
	}
	switch {
	case !categoryFound:
		return "", userErrorf("**%s**'s ticket category is gone. Please let an admin know.", tt.Name)
	case len(channels) >= guildChannelLimit:
		return "", userErrorf("The server is at Discord's limit of %d channels, so no ticket can open. Please let an admin know.", guildChannelLimit)
	case inCategory >= categoryChannelLimit:
		return "", userErrorf("**%s**'s ticket category is full. Please let an admin know.", tt.Name)
	}
	id, number, err := tickets.CreateTicket(&ticket{GuildID: tt.GuildID, TypeID: tt.ID, UserID: member.User.ID, Answers: dumpAnswers(answers)})
	if err != nil {
		return "", err
	}
	ch, err := s.GuildChannelCreateComplex(tt.GuildID, discordgo.GuildChannelCreateData{
		Name:                 ticketChannelName("ticket", number, member.User.Username),
		Type:                 discordgo.ChannelTypeGuildText,
		Topic:                truncateRunes(fmt.Sprintf("%s ticket #%04d for %s (%s)", tt.Name, number, member.User.Username, member.User.ID), 1024),
		ParentID:             tt.CategoryID,
		PermissionOverwrites: ticketOverwrites(tt.GuildID, s.State.User.ID, member.User.ID, staff),
	}, discordgo.WithAuditLogReason(truncateRunes(fmt.Sprintf("Ticket #%04d (%s) opened by %s", number, tt.Name, member.User.Username), 500)))
	if err != nil {
		_ = tickets.DeleteTicket(id)
		appLogger.Warnf("Could not make a ticket channel: %v", err)
		return "", userErrorf("Discord wouldn't let me make your ticket channel. Please let an admin know.")
	}
	if err := tickets.SetTicketChannel(id, ch.ID); err != nil {
		appLogger.Errorf("Ticket %d: could not save its channel: %v", id, err)
	}
	t := &ticket{ID: id, GuildID: tt.GuildID, Number: number, TypeID: tt.ID, UserID: member.User.ID, ChannelID: ch.ID, Status: ticketOpen, Answers: dumpAnswers(answers), OpenedAt: time.Now()}
	if _, err := s.ChannelMessageSendComplex(ch.ID, openingMessage(tt, t, staff, member)); err != nil {
		appLogger.Warnf("Ticket %d: could not post its opening message: %v", id, err)
	}
	if tt.Welcome != "" {
		text := fillWelcome(tt.Welcome, "<@"+member.User.ID+">", displayName(member), tt.Name)
		if _, err := s.ChannelMessageSendComplex(ch.ID, &discordgo.MessageSend{Content: text, AllowedMentions: &discordgo.MessageAllowedMentions{Users: []string{member.User.ID}}}); err != nil {
			appLogger.Warnf("Ticket %d: could not post the welcome message: %v", id, err)
		}
	}
	logTicket(s, tt, &discordgo.MessageEmbed{
		Title:       fmt.Sprintf("🎫 Ticket #%04d opened", number),
		Description: fmt.Sprintf("**Type:** %s\n**By:** <@%s>\n**Channel:** <#%s>", tt.Name, member.User.ID, ch.ID),
		Color:       colorOpened,
	}, nil)
	return ch.ID, nil
}

func displayName(m *discordgo.Member) string {
	switch {
	case m == nil || m.User == nil:
		return "there"
	case m.Nick != "":
		return m.Nick
	case m.User.GlobalName != "":
		return m.User.GlobalName
	}
	return m.User.Username
}

// openingMessage is the first message in a ticket: who opened it, their answers, and the controls.
func openingMessage(tt *ticketType, t *ticket, staff []string, member *discordgo.Member) *discordgo.MessageSend {
	content := fmt.Sprintf("<@%s>, thanks for opening a ticket. %s will be with you here soon.", t.UserID, mentionAll(staff))
	mentions := &discordgo.MessageAllowedMentions{Users: []string{t.UserID}}
	if tt.PingStaff {
		mentions.Roles = staff
	}
	lines := []string{fmt.Sprintf("**Opened by:** <@%s>", t.UserID)}
	if member != nil && member.User != nil {
		if !member.JoinedAt.IsZero() {
			lines = append(lines, fmt.Sprintf("**Joined the server:** <t:%d:R>", member.JoinedAt.Unix()))
		}
		if made, err := discordgo.SnowflakeTimestamp(member.User.ID); err == nil {
			lines = append(lines, fmt.Sprintf("**Account made:** <t:%d:R>", made.Unix()))
		}
	}
	lines = append(lines, "", "Press **🔒 Close** when you're done. Staff can **🙋 Claim** the ticket to show who's handling it.")
	embed := &discordgo.MessageEmbed{
		Title:       truncateRunes(fmt.Sprintf("🎫 %s%s · ticket #%04d", emojiPrefix(tt.Emoji), tt.Name, t.Number), 256),
		Description: strings.Join(lines, "\n"),
		Color:       ticketColor,
	}
	files := answerFields(embed, loadAnswers(t.Answers), fmt.Sprintf("%s ticket #%04d", tt.Name, t.Number), t.UserID)
	return &discordgo.MessageSend{
		Content: content, Embeds: []*discordgo.MessageEmbed{embed}, Files: files, AllowedMentions: mentions,
		Components: []discordgo.MessageComponent{discordgo.ActionsRow{Components: []discordgo.MessageComponent{
			ticketButton(t.ID, "claim"), ticketButton(t.ID, "close"),
		}}},
	}
}

const embedRoom = 5500 // Discord allows 6000 characters across a message's embeds; keep some spare

// answerFields adds the intake answers to an embed, cutting long ones; the full answers
// come back as a file when anything was cut.
func answerFields(embed *discordgo.MessageEmbed, answers []formAnswer, title, from string) []*discordgo.File {
	if len(answers) == 0 {
		return nil
	}
	room := (embedRoom - len(embed.Description) - len(embed.Title)) / len(answers)
	size := max(100, min(1024, room-50))
	cut := false
	for n, a := range answers {
		if n == 25 {
			cut = true
			break
		}
		text := a.Answer
		if text == "" {
			text = "*(no answer)*"
		}
		if len([]rune(text)) > size {
			text, cut = shorten(text, size), true
		}
		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{Name: truncateRunes(fmt.Sprintf("%d. %s", n+1, a.Question), 256), Value: text})
	}
	if !cut {
		return nil
	}
	embed.Footer = &discordgo.MessageEmbedFooter{Text: "Some answers are long: all of them are in the attached file."}
	data := formTranscript(title, "From: "+from, answers)
	return []*discordgo.File{{Name: "answers.txt", ContentType: "text/plain", Reader: strings.NewReader(data)}}
}

func handleMyTicketsButton(s *discordgo.Session, i *discordgo.InteractionCreate, customID string) {
	if customID != "tickets:mine" {
		respondEphemeral(s, i, "This button no longer does anything.")
		return
	}
	if ticketsOff(s, i) {
		return
	}
	types, err := tickets.ListTypes(i.GuildID)
	if err != nil {
		respondEphemeral(s, i, errorText("My tickets", err))
		return
	}
	var lines []string
	for _, tt := range types {
		open, err := tickets.OpenTicketsOf(tt.ID, interactionUser(i).ID)
		if err != nil {
			respondEphemeral(s, i, errorText("My tickets", err))
			return
		}
		for _, t := range open {
			lines = append(lines, fmt.Sprintf("• **%s** #%04d: <#%s>, opened <t:%d:R>", tt.Name, t.Number, t.ChannelID, t.OpenedAt.Unix()))
		}
	}
	text := strings.Join(lines, "\n")
	if text == "" {
		text = "You have no open tickets. Press a button above to open one."
	}
	respondEphemeral(s, i, truncateRunes(text, 1900))
}

// ============================================================================
// /ticket panel: pick the types, then post
// ============================================================================

const ticketPanelMax = 24 // one button per type, plus 🔎 My tickets: Discord allows 25 on a message

type panelDraft struct {
	userID, guildID, channelID, title, text string
	listed                                  []ticketType // the types offered, in order
	chosen                                  []int64
	expires                                 time.Time
}

var (
	panelDraftsMu sync.Mutex
	panelDrafts   = map[string]*panelDraft{}
)

func (p *panelDraft) words(note string) string {
	names := map[int64]string{}
	for _, tt := range p.listed {
		names[tt.ID] = tt.Name
	}
	var picked []string
	for _, id := range p.chosen {
		picked = append(picked, "**"+names[id]+"**")
	}
	list := strings.Join(picked, ", ")
	if list == "" {
		list = "none yet"
	}
	text := fmt.Sprintf("A ticket panel called **%s** for <#%s>. Pick its ticket types (their buttons go in the order listed), then press **Post the panel**.\nPicked: %s",
		p.title, p.channelID, list)
	if note != "" {
		text = note + "\n\n" + text
	}
	return text
}

func (p *panelDraft) components(token string) []discordgo.MessageComponent {
	shown := p.listed
	if len(shown) > 25 {
		shown = shown[:25]
	}
	chosen := map[int64]bool{}
	for _, id := range p.chosen {
		chosen[id] = true
	}
	options := make([]discordgo.SelectMenuOption, len(shown))
	for n, tt := range shown {
		desc := "closed to new tickets for now"
		if tt.Open {
			desc = "taking tickets"
		}
		options[n] = discordgo.SelectMenuOption{Label: truncateRunes(tt.Name, 100), Value: strconv.FormatInt(tt.ID, 10), Description: desc, Default: chosen[tt.ID]}
		if e, err := parseButtonEmoji(tt.Emoji); err == nil && e != nil {
			options[n].Emoji = e
		}
	}
	least := 1
	return []discordgo.MessageComponent{
		discordgo.ActionsRow{Components: []discordgo.MessageComponent{discordgo.SelectMenu{
			MenuType: discordgo.StringSelectMenu, CustomID: "tktpanel:pick:" + token, Placeholder: "Pick the ticket types for the panel…",
			MinValues: &least, MaxValues: min(len(shown), ticketPanelMax), Options: options,
		}}},
		discordgo.ActionsRow{Components: []discordgo.MessageComponent{discordgo.Button{
			Label: "Post the panel", Style: discordgo.SuccessButton, CustomID: "tktpanel:post:" + token, Disabled: len(p.chosen) == 0,
		}}},
	}
}

func ticketPanelMaker(s *discordgo.Session, i *discordgo.InteractionCreate, opts cmdOpts) {
	types, err := tickets.ListTypes(i.GuildID)
	if err != nil {
		respondEphemeral(s, i, errorText("/ticket panel", err))
		return
	}
	if len(types) == 0 {
		respondEphemeral(s, i, "Make some ticket types first, with `/tickettype create`.")
		return
	}
	title, _ := optString(opts, "title")
	text, _ := optString(opts, "text")
	channelID := i.ChannelID
	if ch := optChannel(i, opts, "channel"); ch != nil {
		channelID = ch.ID
	}
	p := &panelDraft{
		userID: interactionUser(i).ID, guildID: i.GuildID, channelID: channelID,
		title: strings.TrimSpace(title), text: panelText(text), listed: types, expires: time.Now().Add(formDraftTTL),
	}
	token := newToken()
	panelDraftsMu.Lock()
	for t, old := range panelDrafts {
		if time.Now().After(old.expires) {
			delete(panelDrafts, t)
		}
	}
	panelDrafts[token] = p
	panelDraftsMu.Unlock()
	err = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Content: p.words(""), Components: p.components(token), Flags: discordgo.MessageFlagsEphemeral},
	})
	if err != nil {
		appLogger.Warnf("Could not answer /ticket panel: %v", err)
	}
}

func handlePanelMaker(s *discordgo.Session, i *discordgo.InteractionCreate, customID string) {
	parts := strings.Split(customID, ":")
	if len(parts) != 3 {
		respondEphemeral(s, i, "This button no longer does anything.")
		return
	}
	token := parts[2]
	panelDraftsMu.Lock()
	p, ok := panelDrafts[token]
	panelDraftsMu.Unlock()
	if !ok || time.Now().After(p.expires) || p.userID != interactionUser(i).ID {
		updateMessage(s, i, "This menu has expired. Run `/ticket panel` again.", nil)
		return
	}
	switch parts[1] {
	case "pick":
		picked := map[string]bool{}
		for _, v := range i.MessageComponentData().Values {
			picked[v] = true
		}
		p.chosen = nil
		for _, tt := range p.listed {
			if picked[strconv.FormatInt(tt.ID, 10)] {
				p.chosen = append(p.chosen, tt.ID)
			}
		}
		updateMessage(s, i, p.words(""), p.components(token))
	case "post":
		said, err := postTicketPanel(s, p)
		if err != nil {
			updateMessage(s, i, p.words("⚠️ "+errorText("Posting a ticket panel", err)), p.components(token))
			return
		}
		panelDraftsMu.Lock()
		delete(panelDrafts, token)
		panelDraftsMu.Unlock()
		updateMessage(s, i, said, nil)
	default:
		respondEphemeral(s, i, "This button no longer does anything.")
	}
}

func postTicketPanel(s *discordgo.Session, p *panelDraft) (string, error) {
	current, err := tickets.ListTypes(p.guildID)
	if err != nil {
		return "", err
	}
	byID := map[int64]ticketType{}
	for _, tt := range current {
		byID[tt.ID] = tt
	}
	var chosen []ticketType
	for _, id := range p.chosen {
		if tt, ok := byID[id]; ok {
			chosen = append(chosen, tt)
		}
	}
	if len(chosen) == 0 {
		return "", userErrorf("Pick at least one ticket type for the panel.")
	}
	if len(chosen) > ticketPanelMax {
		return "", userErrorf("A panel can hold %d ticket types (Discord allows 25 buttons on a message).", ticketPanelMax)
	}
	if err := checkPanelChannel(s, p.channelID); err != nil {
		return "", err
	}
	text := p.text
	if text == "" {
		text = "Press a button below to open a private ticket with staff."
	}
	_, err = s.ChannelMessageSendComplex(p.channelID, &discordgo.MessageSend{
		Embeds:          []*discordgo.MessageEmbed{{Title: p.title, Description: text, Color: ticketColor}},
		Components:      ticketPanelButtons(chosen),
		AllowedMentions: &discordgo.MessageAllowedMentions{},
	})
	if err != nil {
		return "", userErrorf("I couldn't post in <#%s>. Check my permissions there.", p.channelID)
	}
	var closed []string
	for _, tt := range chosen {
		if !tt.Open {
			closed = append(closed, "**"+tt.Name+"**")
		}
	}
	note := ""
	if len(closed) > 0 {
		note = " Closed to new tickets for now: " + strings.Join(closed, ", ") + " (open with `/tickettype edit open:True`)."
	}
	return fmt.Sprintf("✅ Posted a ticket panel with %d type(s) in <#%s>.%s", len(chosen), p.channelID, note), nil
}
