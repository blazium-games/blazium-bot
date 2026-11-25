package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
)

// Inside a ticket: claim, close (with a reason), reopen and delete by button or /ticket,
// and add, remove or rename with /ticket. Closing posts the transcript to the log channel
// and (if the type says so) to the member by DM.
//
// Custom IDs (never change a format already posted on a message):
//
//	tkt:<ticket id>:claim|close|reopen|delete|deleteyes   buttons in a ticket
//	tktclose:<ticket id>                                  the close-reason pop-up

const transcriptMaxMessages = 10000

func ticketButton(id int64, verb string) discordgo.Button {
	custom := fmt.Sprintf("tkt:%d:%s", id, verb)
	switch verb {
	case "claim":
		return discordgo.Button{Label: "🙋 Claim", Style: discordgo.SecondaryButton, CustomID: custom}
	case "close":
		return discordgo.Button{Label: "🔒 Close", Style: discordgo.DangerButton, CustomID: custom}
	case "reopen":
		return discordgo.Button{Label: "🔓 Reopen", Style: discordgo.SuccessButton, CustomID: custom}
	case "delete":
		return discordgo.Button{Label: "🗑️ Delete channel", Style: discordgo.DangerButton, CustomID: custom}
	}
	return discordgo.Button{Label: "Yes, delete it", Style: discordgo.DangerButton, CustomID: custom}
}

// ticketHere is the ticket in this channel and its type, or a mistake to show.
func ticketHere(i *discordgo.InteractionCreate) (*ticket, *ticketType, error) {
	t, err := tickets.TicketByChannel(i.ChannelID)
	if err != nil {
		return nil, nil, err
	}
	if t == nil || t.GuildID != i.GuildID {
		return nil, nil, userErrorf("This isn't a ticket channel. Use this inside a ticket.")
	}
	tt, err := needType(i.GuildID, t.TypeID)
	if err != nil {
		return nil, nil, err
	}
	return t, tt, nil
}

func ticketByID(guildID string, id int64) (*ticket, *ticketType, error) {
	t, err := tickets.GetTicket(guildID, id)
	if err != nil {
		return nil, nil, err
	}
	if t == nil {
		return nil, nil, userErrorf("I can't find that ticket any more.")
	}
	tt, err := needType(guildID, t.TypeID)
	if err != nil {
		return nil, nil, err
	}
	return t, tt, nil
}

func needStaff(i *discordgo.InteractionCreate, tt *ticketType) error {
	ok, err := isTicketStaff(i, tt)
	if err != nil {
		return err
	}
	if !ok {
		return userErrorf("Only **%s** staff can do that.", tt.Name)
	}
	return nil
}

// ============================================================================
// /ticket
// ============================================================================

func handleTicketCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if ticketsOff(s, i) {
		return
	}
	name, opts := commandOptions(i)
	switch name {
	case "panel":
		if !canManageServer(i) {
			respondEphemeral(s, i, "You need Manage Server (or the staff role) to post ticket panels.")
			return
		}
		ticketPanelMaker(s, i, opts)
		return
	case "list":
		if !canManageServer(i) {
			respondEphemeral(s, i, "You need Manage Server (or the staff role) to list tickets.")
			return
		}
	}
	if err := deferEphemeral(s, i); err != nil {
		appLogger.Warnf("Could not defer /ticket: %v", err)
		return
	}
	// Closing reads the whole channel for the transcript, which can outlast the command
	// timeout (a timed-out command is switched off), so the work finishes on its own.
	go runRoute(s, i, "/ticket "+name, func() {
		msg, err := runTicketSubcommand(s, i, name, opts)
		if err != nil {
			msg = errorText("/ticket "+name, err)
		}
		editResponse(s, i, msg)
	})
}

func runTicketSubcommand(s *discordgo.Session, i *discordgo.InteractionCreate, name string, opts cmdOpts) (string, error) {
	if name == "list" {
		return ticketList(i, opts)
	}
	t, tt, err := ticketHere(i)
	if err != nil {
		return "", err
	}
	user := interactionUser(i)
	switch name {
	case "close":
		if user.ID != t.UserID {
			if err := needStaff(i, tt); err != nil {
				return "", userErrorf("Only the member who opened this ticket, or **%s** staff, can close it.", tt.Name)
			}
		}
		reason, _ := optString(opts, "reason")
		return closeTicket(s, t, tt, user.ID, strings.TrimSpace(reason))
	}
	if err := needStaff(i, tt); err != nil {
		return "", err
	}
	switch name {
	case "claim":
		return claimTicket(s, t, tt, user.ID)
	case "add":
		return addToTicket(s, i, opts, t)
	case "remove":
		return removeFromTicket(s, i, opts, t)
	case "rename":
		newName, _ := optString(opts, "name")
		return renameTicket(s, t, newName)
	}
	return "", userErrorf("Unknown subcommand.")
}

func ticketList(i *discordgo.InteractionCreate, opts cmdOpts) (string, error) {
	typeID, _ := optInt(opts, "type")
	status, ok := optString(opts, "status")
	if !ok {
		status = ticketOpen
	}
	types, err := tickets.ListTypes(i.GuildID)
	if err != nil {
		return "", err
	}
	names := map[int64]string{}
	for _, tt := range types {
		names[tt.ID] = tt.Name
	}
	found, err := tickets.ListTickets(i.GuildID, typeID, status)
	if err != nil {
		return "", err
	}
	if len(found) == 0 {
		return fmt.Sprintf("No %s tickets.", status), nil
	}
	var lines []string
	for _, t := range found {
		line := fmt.Sprintf("#%04d · %s · <@%s> · opened <t:%d:R>", t.Number, names[t.TypeID], t.UserID, t.OpenedAt.Unix())
		if t.Status != ticketDeleted && t.ChannelID != "" {
			line += " · <#" + t.ChannelID + ">"
		}
		if t.ClaimedBy != "" {
			line += " · claimed by <@" + t.ClaimedBy + ">"
		}
		if t.Status != ticketOpen && t.CloseReason != "" {
			line += " · " + shorten(t.CloseReason, 60)
		}
		lines = append(lines, line)
	}
	return fitLines(fmt.Sprintf("**%d %s ticket(s), newest first:**", len(found), status), lines, func(left int) string {
		return fmt.Sprintf("…and %d more.", left)
	}), nil
}

// ============================================================================
// BUTTONS
// ============================================================================

func handleTicketControl(s *discordgo.Session, i *discordgo.InteractionCreate, customID string) {
	parts := strings.Split(customID, ":")
	id, ok := parseID(parts, 1)
	if !ok || len(parts) != 3 {
		respondEphemeral(s, i, "This button no longer does anything.")
		return
	}
	if ticketsOff(s, i) {
		return
	}
	if err := ticketControl(s, i, id, parts[2]); err != nil {
		replyEphemeral(s, i, errorText("Ticket button", err))
	}
}

func ticketControl(s *discordgo.Session, i *discordgo.InteractionCreate, id int64, verb string) error {
	t, tt, err := ticketByID(i.GuildID, id)
	if err != nil {
		return err
	}
	user := interactionUser(i)
	if verb == "close" {
		if user.ID != t.UserID {
			if err := needStaff(i, tt); err != nil {
				return userErrorf("Only the member who opened this ticket, or **%s** staff, can close it.", tt.Name)
			}
		}
		if t.Status != ticketOpen {
			return userErrorf("This ticket is already closed.")
		}
		required := false
		return showModal(s, i, &discordgo.InteractionResponseData{
			CustomID: fmt.Sprintf("tktclose:%d", t.ID),
			Title:    fmt.Sprintf("Close ticket #%04d", t.Number),
			Components: []discordgo.MessageComponent{discordgo.Label{
				Label: "Why is it being closed? (optional)", Description: "Shown in the ticket log and the transcript DM.",
				Component: discordgo.TextInput{CustomID: "reason", Style: discordgo.TextInputParagraph, Required: &required, MaxLength: formReasonMax},
			}},
		})
	}
	if err := needStaff(i, tt); err != nil {
		return err
	}
	switch verb {
	case "claim":
		if err := deferEphemeral(s, i); err != nil {
			return err
		}
		msg, err := claimTicket(s, t, tt, user.ID)
		if err != nil {
			msg = errorText("Claiming a ticket", err)
		}
		editResponse(s, i, msg)
	case "reopen":
		if err := deferEphemeral(s, i); err != nil {
			return err
		}
		msg, err := reopenTicket(s, t, tt, user.ID)
		if err != nil {
			msg = errorText("Reopening a ticket", err)
		}
		editResponse(s, i, msg)
	case "delete":
		if t.Status != ticketClosed {
			return userErrorf("Close the ticket first, so its transcript is saved.")
		}
		err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "Delete this channel? Its transcript is already in the log channel. This can't be undone.",
				Flags:   discordgo.MessageFlagsEphemeral,
				Components: []discordgo.MessageComponent{discordgo.ActionsRow{Components: []discordgo.MessageComponent{
					ticketButton(t.ID, "deleteyes"),
				}}},
			},
		})
		return err
	case "deleteyes":
		if t.Status != ticketClosed {
			return userErrorf("This ticket was reopened, so it wasn't deleted.")
		}
		updateMessage(s, i, "Deleting…", nil)
		deleteTicketChannel(s, t, tt, user.ID)
	default:
		respondEphemeral(s, i, "This button no longer does anything.")
	}
	return nil
}

func handleCloseReasonSubmit(s *discordgo.Session, i *discordgo.InteractionCreate, customID string) {
	id, ok := parseID(strings.Split(customID, ":"), 1)
	if !ok {
		respondEphemeral(s, i, "That pop-up has expired. Please try again.")
		return
	}
	if ticketsOff(s, i) {
		return
	}
	t, tt, err := ticketByID(i.GuildID, id)
	if err == nil && interactionUser(i).ID != t.UserID {
		if staffErr := needStaff(i, tt); staffErr != nil {
			err = userErrorf("Only the member who opened this ticket, or **%s** staff, can close it.", tt.Name)
		}
	}
	if err != nil {
		respondEphemeral(s, i, errorText("Closing a ticket", err))
		return
	}
	if err := deferEphemeral(s, i); err != nil {
		appLogger.Warnf("Could not defer a ticket close: %v", err)
		return
	}
	reason := strings.TrimSpace(modalValues(i)["reason"].text())
	msg, err := closeTicket(s, t, tt, interactionUser(i).ID, reason)
	if err != nil {
		msg = errorText("Closing a ticket", err)
	}
	editResponse(s, i, msg)
}

// ============================================================================
// ACTIONS
// ============================================================================

func postInTicket(s *discordgo.Session, t *ticket, msg *discordgo.MessageSend) {
	if msg.AllowedMentions == nil {
		msg.AllowedMentions = &discordgo.MessageAllowedMentions{}
	}
	if _, err := s.ChannelMessageSendComplex(t.ChannelID, msg); err != nil {
		appLogger.Debugf("Ticket %d: could not post in its channel: %v", t.ID, err)
	}
}

// logTicket posts a note in the type's log channel.
func logTicket(s *discordgo.Session, tt *ticketType, embed *discordgo.MessageEmbed, files []*discordgo.File) {
	embed.Timestamp = time.Now().UTC().Format(time.RFC3339)
	_, err := s.ChannelMessageSendComplex(tt.LogChannelID, &discordgo.MessageSend{
		Embeds: []*discordgo.MessageEmbed{embed}, Files: files, AllowedMentions: &discordgo.MessageAllowedMentions{},
	})
	if err != nil {
		appLogger.Warnf("Could not log to %s's log channel: %v", tt.Name, err)
	}
}

func claimTicket(s *discordgo.Session, t *ticket, tt *ticketType, userID string) (string, error) {
	if t.Status != ticketOpen {
		return "", userErrorf("This ticket is closed.")
	}
	if t.ClaimedBy == userID {
		if err := tickets.SetClaim(t.ID, ""); err != nil {
			return "", err
		}
		postInTicket(s, t, &discordgo.MessageSend{Content: fmt.Sprintf("<@%s> unclaimed this ticket.", userID)})
		return "You unclaimed this ticket.", nil
	}
	previous := t.ClaimedBy
	if err := tickets.SetClaim(t.ID, userID); err != nil {
		return "", err
	}
	words := fmt.Sprintf("🙋 <@%s> claimed this ticket and will handle it.", userID)
	if previous != "" {
		words = fmt.Sprintf("🙋 <@%s> took this ticket over from <@%s>.", userID, previous)
	}
	postInTicket(s, t, &discordgo.MessageSend{Content: words})
	logTicket(s, tt, &discordgo.MessageEmbed{
		Title:       fmt.Sprintf("🙋 Ticket #%04d claimed", t.Number),
		Description: fmt.Sprintf("**Type:** %s\n**By:** <@%s>\n**Channel:** <#%s>", tt.Name, userID, t.ChannelID),
		Color:       colorNotice,
	}, nil)
	return "You claimed this ticket.", nil
}

func addToTicket(s *discordgo.Session, i *discordgo.InteractionCreate, opts cmdOpts, t *ticket) (string, error) {
	o, ok := opts["user"]
	if !ok {
		return "", userErrorf("Pick someone to add.")
	}
	userID, _ := o.Value.(string)
	if t.Status != ticketOpen {
		return "", userErrorf("This ticket is closed. Reopen it first.")
	}
	if err := s.ChannelPermissionSet(t.ChannelID, userID, discordgo.PermissionOverwriteTypeMember, chatTalk, 0); err != nil {
		return "", err
	}
	postInTicket(s, t, &discordgo.MessageSend{
		Content:         fmt.Sprintf("➕ <@%s> added <@%s> to this ticket.", interactionUser(i).ID, userID),
		AllowedMentions: &discordgo.MessageAllowedMentions{Users: []string{userID}},
	})
	return fmt.Sprintf("Added <@%s>.", userID), nil
}

func removeFromTicket(s *discordgo.Session, i *discordgo.InteractionCreate, opts cmdOpts, t *ticket) (string, error) {
	o, ok := opts["user"]
	if !ok {
		return "", userErrorf("Pick someone to remove.")
	}
	userID, _ := o.Value.(string)
	if userID == t.UserID {
		return "", userErrorf("That's the member who opened the ticket. Close the ticket instead.")
	}
	if userID == s.State.User.ID {
		return "", userErrorf("I need to stay in the ticket to run it.")
	}
	if err := s.ChannelPermissionDelete(t.ChannelID, userID); err != nil && !isNotFound(err) {
		return "", err
	}
	postInTicket(s, t, &discordgo.MessageSend{Content: fmt.Sprintf("➖ <@%s> removed <@%s> from this ticket.", interactionUser(i).ID, userID)})
	return fmt.Sprintf("Removed <@%s>. (Staff roles still see the ticket.)", userID), nil
}

// cleanChannelName makes text a valid channel name: lower case, dashes for spaces.
func cleanChannelName(text string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(text)) {
		switch {
		case r == ' ' || r == '-' || r == '.':
			if !lastDash && b.Len() > 0 {
				b.WriteRune('-')
				lastDash = true
			}
		case r == '_' || ('a' <= r && r <= 'z') || ('0' <= r && r <= '9') || r > 127:
			b.WriteRune(r)
			lastDash = false
		}
	}
	return truncateRunes(strings.Trim(b.String(), "-"), 90)
}

func renameTicket(s *discordgo.Session, t *ticket, text string) (string, error) {
	name := cleanChannelName(text)
	if name == "" {
		return "", userErrorf("Use letters or numbers in the name.")
	}
	if _, err := s.ChannelEdit(t.ChannelID, &discordgo.ChannelEdit{Name: name}); err != nil {
		return "", userErrorf("Discord wouldn't rename the channel (it allows two renames every ten minutes). Try again later.")
	}
	return fmt.Sprintf("Renamed to **#%s**.", name), nil
}

// setMembersAccess gives every member overwrite in a ticket (the opener and anyone added)
// talk access, or takes their access away. Staff roles and the bot keep theirs.
func setMembersAccess(s *discordgo.Session, t *ticket, open bool) {
	ch, err := s.Channel(t.ChannelID)
	if err != nil {
		appLogger.Warnf("Ticket %d: could not read its channel: %v", t.ID, err)
		return
	}
	members := map[string]bool{t.UserID: true}
	for _, o := range ch.PermissionOverwrites {
		if o.Type == discordgo.PermissionOverwriteTypeMember && o.ID != s.State.User.ID {
			members[o.ID] = true
		}
	}
	for id := range members {
		allow, deny := int64(chatTalk), int64(0)
		if !open {
			allow, deny = 0, discordgo.PermissionViewChannel
		}
		if err := s.ChannelPermissionSet(t.ChannelID, id, discordgo.PermissionOverwriteTypeMember, allow, deny); err != nil {
			appLogger.Warnf("Ticket %d: could not change access for %s: %v", t.ID, id, err)
		}
	}
}

func usernameOf(s *discordgo.Session, guildID, userID string) string {
	if m := lookupMember(s, guildID, userID); m != nil && m.User != nil {
		return m.User.Username
	}
	return userID
}

func closeTicket(s *discordgo.Session, t *ticket, tt *ticketType, by, reason string) (string, error) {
	done, err := tickets.Close(t.ID, by, reason)
	if err != nil {
		return "", err
	}
	if !done {
		return "", userErrorf("This ticket is already closed.")
	}
	now := time.Now().UTC()
	t.Status, t.ClosedBy, t.CloseReason, t.ClosedAt = ticketClosed, by, reason, &now

	messages, fetchErr := fetchTicketMessages(s, t.ChannelID)
	if fetchErr != nil {
		appLogger.Warnf("Ticket %d: could not read its messages for the transcript: %v", t.ID, fetchErr)
	}
	transcript := renderTranscript(tt, t, usernameOf(s, t.GuildID, t.UserID), messages)
	fileName := fmt.Sprintf("ticket-%04d-transcript.txt", t.Number)

	setMembersAccess(s, t, false)
	if _, err := s.ChannelEdit(t.ChannelID, &discordgo.ChannelEdit{Name: ticketChannelName("closed", t.Number, usernameOf(s, t.GuildID, t.UserID))}); err != nil {
		appLogger.Debugf("Ticket %d: not renamed on close: %v", t.ID, err)
	}
	words := fmt.Sprintf("🔒 Ticket closed by <@%s>.", by)
	if reason != "" {
		words += "\n**Reason:** " + reason
	}
	postInTicket(s, t, &discordgo.MessageSend{
		Content: words,
		Components: []discordgo.MessageComponent{discordgo.ActionsRow{Components: []discordgo.MessageComponent{
			ticketButton(t.ID, "reopen"), ticketButton(t.ID, "delete"),
		}}},
	})

	summary := closeSummary(tt, t, len(messages))
	logTicket(s, tt, summary, []*discordgo.File{{Name: fileName, ContentType: "text/plain", Reader: strings.NewReader(transcript)}})

	told := ""
	if tt.DMTranscript {
		dmEmbed := closeSummary(tt, t, len(messages))
		dmEmbed.Title = fmt.Sprintf("🔒 Your %s ticket #%04d was closed", tt.Name, t.Number)
		if g, err := lookupGuild(s, t.GuildID); err == nil {
			dmEmbed.Footer = &discordgo.MessageEmbedFooter{Text: g.Name}
		}
		dm, err := s.UserChannelCreate(t.UserID)
		if err == nil {
			_, err = s.ChannelMessageSendComplex(dm.ID, &discordgo.MessageSend{
				Embeds: []*discordgo.MessageEmbed{dmEmbed},
				Files:  []*discordgo.File{{Name: fileName, ContentType: "text/plain", Reader: strings.NewReader(transcript)}},
			})
		}
		if err != nil {
			told = " The member's DMs are closed, so they didn't get the transcript."
		} else {
			told = " The member got the transcript by DM."
		}
	}
	note := ""
	if fetchErr != nil {
		note = " ⚠️ I couldn't read the channel's messages, so the transcript is incomplete."
	} else if missingContent(messages, s.State.User.ID) {
		note = " ⚠️ The transcript has no message text: turn on the bot's Message Content intent in the Discord Developer Portal."
	}
	return fmt.Sprintf("Closed ticket #%04d. The transcript is in <#%s>.%s%s", t.Number, tt.LogChannelID, told, note), nil
}

func closeSummary(tt *ticketType, t *ticket, messageCount int) *discordgo.MessageEmbed {
	lines := []string{
		"**Type:** " + tt.Name,
		"**Opened by:** <@" + t.UserID + ">",
		fmt.Sprintf("**Opened:** <t:%d:f>", t.OpenedAt.Unix()),
		"**Closed by:** <@" + t.ClosedBy + ">",
	}
	if t.ClaimedBy != "" {
		lines = append(lines, "**Claimed by:** <@"+t.ClaimedBy+">")
	}
	if t.CloseReason != "" {
		lines = append(lines, "**Reason:** "+t.CloseReason)
	}
	lines = append(lines, fmt.Sprintf("**Messages:** %d", messageCount))
	return &discordgo.MessageEmbed{
		Title:       fmt.Sprintf("🔒 Ticket #%04d closed", t.Number),
		Description: strings.Join(lines, "\n"),
		Color:       colorClosed,
	}
}

func reopenTicket(s *discordgo.Session, t *ticket, tt *ticketType, by string) (string, error) {
	done, err := tickets.Reopen(t.ID)
	if err != nil {
		return "", err
	}
	if !done {
		return "", userErrorf("This ticket isn't closed.")
	}
	setMembersAccess(s, t, true)
	if _, err := s.ChannelEdit(t.ChannelID, &discordgo.ChannelEdit{Name: ticketChannelName("ticket", t.Number, usernameOf(s, t.GuildID, t.UserID))}); err != nil {
		appLogger.Debugf("Ticket %d: not renamed on reopen: %v", t.ID, err)
	}
	postInTicket(s, t, &discordgo.MessageSend{
		Content:         fmt.Sprintf("🔓 <@%s> reopened this ticket, <@%s>.", by, t.UserID),
		AllowedMentions: &discordgo.MessageAllowedMentions{Users: []string{t.UserID}},
		Components: []discordgo.MessageComponent{discordgo.ActionsRow{Components: []discordgo.MessageComponent{
			ticketButton(t.ID, "claim"), ticketButton(t.ID, "close"),
		}}},
	})
	logTicket(s, tt, &discordgo.MessageEmbed{
		Title:       fmt.Sprintf("🔓 Ticket #%04d reopened", t.Number),
		Description: fmt.Sprintf("**Type:** %s\n**By:** <@%s>\n**Channel:** <#%s>", tt.Name, by, t.ChannelID),
		Color:       colorOpened,
	}, nil)
	return "Reopened the ticket.", nil
}

func deleteTicketChannel(s *discordgo.Session, t *ticket, tt *ticketType, by string) {
	if _, err := s.ChannelDelete(t.ChannelID, discordgo.WithAuditLogReason(fmt.Sprintf("Ticket #%04d deleted", t.Number))); err != nil && !isNotFound(err) {
		appLogger.Warnf("Ticket %d: could not delete its channel: %v", t.ID, err)
		return
	}
	if err := tickets.MarkDeleted(t.ID); err != nil {
		appLogger.Warnf("Ticket %d: could not mark it deleted: %v", t.ID, err)
	}
	logTicket(s, tt, &discordgo.MessageEmbed{
		Title:       fmt.Sprintf("🗑️ Ticket #%04d channel deleted", t.Number),
		Description: fmt.Sprintf("**Type:** %s\n**By:** <@%s>", tt.Name, by),
		Color:       colorClosed,
	}, nil)
}

// ============================================================================
// TRANSCRIPTS
// ============================================================================

// fetchTicketMessages reads a channel's messages, oldest first.
func fetchTicketMessages(s *discordgo.Session, channelID string) ([]*discordgo.Message, error) {
	var all []*discordgo.Message
	before := ""
	for len(all) < transcriptMaxMessages {
		batch, err := s.ChannelMessages(channelID, 100, before, "", "")
		if err != nil {
			return reverseMessages(all), err
		}
		all = append(all, batch...)
		if len(batch) < 100 {
			break
		}
		before = batch[len(batch)-1].ID
	}
	return reverseMessages(all), nil
}

func reverseMessages(in []*discordgo.Message) []*discordgo.Message {
	out := make([]*discordgo.Message, len(in))
	for n, m := range in {
		out[len(in)-1-n] = m
	}
	return out
}

// missingContent reports whether members' messages came back without text, which is what
// Discord does when the bot lacks the Message Content intent.
func missingContent(messages []*discordgo.Message, botID string) bool {
	fromMembers, withText := 0, 0
	for _, m := range messages {
		if m.Author == nil || m.Author.ID == botID || m.Author.Bot {
			continue
		}
		fromMembers++
		if m.Content != "" || len(m.Attachments) > 0 || len(m.Embeds) > 0 {
			withText++
		}
	}
	return fromMembers > 0 && withText == 0
}

// renderTranscript is a ticket as plain text: a header, then every message with its time and author.
func renderTranscript(tt *ticketType, t *ticket, opener string, messages []*discordgo.Message) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s ticket #%04d\n", tt.Name, t.Number)
	fmt.Fprintf(&b, "Opened by %s (%s) at %s\n", opener, t.UserID, t.OpenedAt.UTC().Format("2006-01-02 15:04 UTC"))
	if t.ClaimedBy != "" {
		fmt.Fprintf(&b, "Claimed by %s\n", t.ClaimedBy)
	}
	if t.ClosedAt != nil {
		fmt.Fprintf(&b, "Closed by %s at %s\n", t.ClosedBy, t.ClosedAt.UTC().Format("2006-01-02 15:04 UTC"))
	}
	if t.CloseReason != "" {
		fmt.Fprintf(&b, "Reason: %s\n", t.CloseReason)
	}
	if answers := loadAnswers(t.Answers); len(answers) > 0 {
		b.WriteString("\nAnswers:\n")
		for n, a := range answers {
			answer := a.Answer
			if answer == "" {
				answer = "(no answer)"
			}
			fmt.Fprintf(&b, "%d. %s\n   %s\n", n+1, a.Question, strings.ReplaceAll(answer, "\n", "\n   "))
		}
	}
	fmt.Fprintf(&b, "\nMessages (%d):\n", len(messages))
	for _, m := range messages {
		author := "unknown"
		if m.Author != nil {
			author = m.Author.Username
			if m.Author.Bot {
				author += " [bot]"
			}
		}
		fmt.Fprintf(&b, "\n[%s] %s:", m.Timestamp.UTC().Format("2006-01-02 15:04"), author)
		if m.Content != "" {
			b.WriteString(" " + strings.ReplaceAll(m.Content, "\n", "\n    "))
		}
		for _, e := range m.Embeds {
			parts := []string{}
			for _, p := range []string{e.Title, e.Description} {
				if p != "" {
					parts = append(parts, p)
				}
			}
			for _, f := range e.Fields {
				parts = append(parts, f.Name+": "+f.Value)
			}
			if len(parts) > 0 {
				b.WriteString("\n    [embed] " + strings.ReplaceAll(strings.Join(parts, " | "), "\n", " "))
			}
		}
		for _, a := range m.Attachments {
			fmt.Fprintf(&b, "\n    [file] %s %s", a.Filename, a.URL)
		}
	}
	b.WriteString("\n")
	return b.String()
}
