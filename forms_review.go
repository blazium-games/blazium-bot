package main

import (
	"fmt"
	"strings"

	"github.com/bwmarrin/discordgo"
)

// Reviewing: each application is posted in the form's review channel with ✅ Accept,
// ❌ Decline and the staff vote buttons. Accept and Decline ask for an optional note to
// the applicant, then give the accept role (if any) and tell the applicant by DM.
//
// Custom IDs (never change a format already posted on a message):
//
//	application:<entry id>:accept|decline|yes|no|unsure   buttons on a review message
//	appnote:<entry id>:accept|decline                     the note pop-up

const embedRoom = 5500 // Discord allows 6000 characters across a message's embeds; keep some spare

var voteVerbs = []struct{ Key, Label string }{
	{"yes", "👍 Yes"},
	{"no", "👎 No"},
	{"unsure", "🤷 Unsure"},
}

func voteLabel(key string) string {
	for _, v := range voteVerbs {
		if v.Key == key {
			return v.Label
		}
	}
	return key
}

// voteTally is "👍 2 · 👎 1 · 🤷 0", with who voted what underneath when names.
func voteTally(votes []appVote, names bool) string {
	var parts []string
	for _, v := range voteVerbs {
		count := 0
		for _, cast := range votes {
			if cast.Vote == v.Key {
				count++
			}
		}
		emoji, _, _ := strings.Cut(v.Label, " ")
		parts = append(parts, fmt.Sprintf("%s %d", emoji, count))
	}
	line := strings.Join(parts, " · ")
	if !names {
		return line
	}
	if len(votes) == 0 {
		return line + "\nNo votes yet."
	}
	for _, v := range voteVerbs {
		var who []string
		for _, cast := range votes {
			if cast.Vote == v.Key {
				who = append(who, "<@"+cast.UserID+">")
			}
		}
		if len(who) > 0 {
			emoji, _, _ := strings.Cut(v.Label, " ")
			line += "\n" + emoji + " " + strings.Join(who, ", ")
		}
	}
	return truncateRunes(line, 1024)
}

func reviewButton(entryID int64, verb string, count int) discordgo.Button {
	id := fmt.Sprintf("application:%d:%s", entryID, verb)
	switch verb {
	case "accept":
		return discordgo.Button{Label: "✅ Accept", Style: discordgo.SuccessButton, CustomID: id}
	case "decline":
		return discordgo.Button{Label: "❌ Decline", Style: discordgo.DangerButton, CustomID: id}
	}
	return discordgo.Button{Label: fmt.Sprintf("%s · %d", voteLabel(verb), count), Style: discordgo.SecondaryButton, CustomID: id}
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

// reviewMessage is the application as staff see it: answers, who sent it, its status and
// (staff) the votes and buttons. Without staff it's the copy posted in the applicant's own
// chat: no votes, no buttons.
func reviewMessage(s *discordgo.Session, f *appForm, e *appEntry, votes []appVote, staff bool) *discordgo.MessageSend {
	var member *discordgo.Member
	if s != nil {
		member = lookupMember(s, e.GuildID, e.UserID)
	}
	return buildReviewMessage(f, e, member, votes, staff)
}

func buildReviewMessage(f *appForm, e *appEntry, member *discordgo.Member, votes []appVote, staff bool) *discordgo.MessageSend {
	answers := loadAnswers(e.Answers)
	who := "<@" + e.UserID + ">"
	if member != nil && member.User != nil {
		who += " (" + member.User.Username + ")"
	} else {
		who += " (has left the server)"
	}
	lines := []string{"**From:** " + who}
	if member != nil {
		if !member.JoinedAt.IsZero() {
			lines = append(lines, fmt.Sprintf("**Joined the server:** <t:%d:R>", member.JoinedAt.Unix()))
		}
		if made, err := discordgo.SnowflakeTimestamp(e.UserID); err == nil {
			lines = append(lines, fmt.Sprintf("**Account made:** <t:%d:R>", made.Unix()))
		}
	}
	lines = append(lines, "**Status:** "+entryStatusWords[e.Status])
	if e.ChatChannelID != "" {
		lines = append(lines, "**Chat:** <#"+e.ChatChannelID+">")
	}
	if e.Status != entryPending {
		lines = append(lines, "**Decided by:** <@"+e.DecidedBy+">")
		if e.Note != "" {
			lines = append(lines, "**Note to the applicant:** "+e.Note)
		}
	}
	color := formColor
	switch e.Status {
	case entryAccepted:
		color = colorAccepted
	case entryDeclined:
		color = colorDeclined
	}
	embed := &discordgo.MessageEmbed{
		Title:       truncateRunes(fmt.Sprintf("📝 %s: application #%d", f.Name, e.ID), 256),
		Description: strings.Join(lines, "\n"),
		Color:       color,
	}
	room := (embedRoom - len(embed.Description) - len(embed.Title)) / max(1, len(answers))
	size := max(100, min(1024, room-50))
	cut := false
	for n, a := range answers {
		if n == 25 {
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
	msg := &discordgo.MessageSend{Embeds: []*discordgo.MessageEmbed{embed}, AllowedMentions: &discordgo.MessageAllowedMentions{}}
	if cut || len(answers) > 25 {
		from := e.UserID
		if member != nil && member.User != nil {
			from = member.User.Username
		}
		data := formTranscript(fmt.Sprintf("%s: application #%d", f.Name, e.ID), "From: "+from, answers)
		msg.Files = []*discordgo.File{{Name: fmt.Sprintf("application-%d.txt", e.ID), ContentType: "text/plain", Reader: strings.NewReader(data)}}
		embed.Footer = &discordgo.MessageEmbedFooter{Text: "Some answers are long: the full application is in the attached file."}
	}
	if !staff {
		return msg
	}
	embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{Name: "🗳️ Staff votes", Value: voteTally(votes, true)})
	if e.Status == entryPending {
		row2 := discordgo.ActionsRow{}
		for _, v := range voteVerbs {
			count := 0
			for _, cast := range votes {
				if cast.Vote == v.Key {
					count++
				}
			}
			row2.Components = append(row2.Components, reviewButton(e.ID, v.Key, count))
		}
		msg.Components = []discordgo.MessageComponent{
			discordgo.ActionsRow{Components: []discordgo.MessageComponent{reviewButton(e.ID, "accept", 0), reviewButton(e.ID, "decline", 0)}},
			row2,
		}
	}
	return msg
}

func postForReview(s *discordgo.Session, f *appForm, entryID int64) {
	e, err := appForms.GetEntry(f.GuildID, entryID)
	if err != nil || e == nil {
		appLogger.Warnf("Application %d: could not load it for review: %v", entryID, err)
		return
	}
	votes, _ := appForms.Votes(entryID)
	msg, err := s.ChannelMessageSendComplex(f.ReviewChannelID, reviewMessage(s, f, e, votes, true))
	if err != nil {
		appLogger.Errorf("Application %d: could not post it for review in %s: %v", entryID, f.ReviewChannelID, err)
		return
	}
	if err := appForms.SetReviewMessage(entryID, msg.ChannelID, msg.ID); err != nil {
		appLogger.Warnf("Application %d: could not save its review message: %v", entryID, err)
	}
}

// refreshReview redraws the review channel's copy of an application.
func refreshReview(s *discordgo.Session, f *appForm, entryID int64) {
	e, err := appForms.GetEntry(f.GuildID, entryID)
	if err != nil || e == nil || e.ReviewMessageID == "" {
		return
	}
	votes, _ := appForms.Votes(entryID)
	msg := reviewMessage(s, f, e, votes, true)
	components := msg.Components
	if components == nil {
		components = []discordgo.MessageComponent{}
	}
	_, err = s.ChannelMessageEditComplex(&discordgo.MessageEdit{ID: e.ReviewMessageID, Channel: e.ReviewChannelID, Embeds: &msg.Embeds, Components: &components})
	if err != nil {
		appLogger.Debugf("Application %d: review message not updated: %v", entryID, err) // the decision is saved; the message is only a picture of it
	}
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

// canReview: Manage Server (or the staff role), or the form's reviewer role.
func canReview(i *discordgo.InteractionCreate, f *appForm) bool {
	return canManageServer(i) || hasRole(i.Member, f.ReviewerRoleID)
}

// canVote: reviewers, and the form's chat staff roles.
func canVote(i *discordgo.InteractionCreate, f *appForm) (bool, error) {
	if canReview(i, f) {
		return true, nil
	}
	staff, err := appForms.ChatRoles(f.ID)
	if err != nil {
		return false, err
	}
	for _, id := range staff {
		if hasRole(i.Member, id) {
			return true, nil
		}
	}
	return false, nil
}

func handleReviewButton(s *discordgo.Session, i *discordgo.InteractionCreate, customID string) {
	parts := strings.Split(customID, ":")
	id, ok := parseID(parts, 1)
	if !ok || len(parts) != 3 {
		respondEphemeral(s, i, "This button no longer does anything.")
		return
	}
	if formsOff(s, i) {
		return
	}
	verb := parts[2]
	var err error
	switch verb {
	case "accept", "decline":
		err = askNote(s, i, id, verb)
	case "yes", "no", "unsure":
		err = castVote(s, i, id, verb)
	default:
		respondEphemeral(s, i, "This button no longer does anything.")
		return
	}
	if err != nil {
		replyEphemeral(s, i, errorText("Review button", err))
	}
}

func entryAndForm(guildID string, entryID int64) (*appEntry, *appForm, error) {
	e, err := appForms.GetEntry(guildID, entryID)
	if err != nil {
		return nil, nil, err
	}
	if e == nil {
		return nil, nil, userErrorf("I can't find that application any more.")
	}
	f, err := needForm(guildID, e.FormID)
	if err != nil {
		return nil, nil, err
	}
	return e, f, nil
}

// castVote: press a vote to cast or change it, press the same one again to take it back.
func castVote(s *discordgo.Session, i *discordgo.InteractionCreate, entryID int64, verb string) error {
	e, f, err := entryAndForm(i.GuildID, entryID)
	if err != nil {
		return err
	}
	allowed, err := canVote(i, f)
	if err != nil {
		return err
	}
	if !allowed {
		return userErrorf("Only staff who review this form can vote on its applications.")
	}
	if e.Status != entryPending {
		return userErrorf("Application #%d was already %s; voting is closed.", entryID, e.Status)
	}
	votes, err := appForms.Votes(entryID)
	if err != nil {
		return err
	}
	userID := interactionUser(i).ID
	mine := ""
	for _, v := range votes {
		if v.UserID == userID {
			mine = v.Vote
		}
	}
	next := verb
	if mine == verb {
		next = ""
	}
	if err := appForms.SetVote(entryID, userID, next); err != nil {
		return err
	}
	votes, err = appForms.Votes(entryID)
	if err != nil {
		return err
	}
	onReviewMessage := i.Message != nil && i.Message.ID == e.ReviewMessageID
	if onReviewMessage {
		msg := reviewMessage(s, f, e, votes, true)
		return s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseUpdateMessage,
			Data: &discordgo.InteractionResponseData{Embeds: msg.Embeds, Components: msg.Components, AllowedMentions: &discordgo.MessageAllowedMentions{}},
		})
	}
	words := "You voted " + voteLabel(verb) + "."
	if next == "" {
		words = "Vote taken back."
	}
	respondEphemeral(s, i, words+" Now "+voteTally(votes, false)+".")
	refreshReview(s, f, entryID) // keep the review channel's copy up to date
	return nil
}

func askNote(s *discordgo.Session, i *discordgo.InteractionCreate, entryID int64, verb string) error {
	e, f, err := entryAndForm(i.GuildID, entryID)
	if err != nil {
		return err
	}
	if !canReview(i, f) {
		return userErrorf("Only staff with Manage Server, or the form's reviewer role, can decide applications.")
	}
	if e.Status != entryPending {
		return userErrorf("Application #%d was already %s.", entryID, e.Status)
	}
	title := "Accept application"
	if verb == "decline" {
		title = "Decline application"
	}
	required := false
	return showModal(s, i, &discordgo.InteractionResponseData{
		CustomID: fmt.Sprintf("appnote:%d:%s", entryID, verb),
		Title:    title,
		Components: []discordgo.MessageComponent{discordgo.Label{
			Label: "A note for the applicant (optional)", Description: "Sent to them by DM with the decision.",
			Component: discordgo.TextInput{CustomID: "note", Style: discordgo.TextInputParagraph, Required: &required, MaxLength: formNoteMax},
		}},
	})
}

func handleNoteSubmit(s *discordgo.Session, i *discordgo.InteractionCreate, customID string) {
	parts := strings.Split(customID, ":")
	id, ok := parseID(parts, 1)
	if !ok || len(parts) != 3 || (parts[2] != "accept" && parts[2] != "decline") {
		respondEphemeral(s, i, "That form has expired. Please try again.")
		return
	}
	if formsOff(s, i) {
		return
	}
	note := strings.TrimSpace(modalValues(i)["note"].text())
	if err := decideApplication(s, i, id, parts[2], note); err != nil {
		replyEphemeral(s, i, errorText("Deciding an application", err))
	}
}

func decideApplication(s *discordgo.Session, i *discordgo.InteractionCreate, entryID int64, verb, note string) error {
	e, f, err := entryAndForm(i.GuildID, entryID)
	if err != nil {
		return err
	}
	if !canReview(i, f) {
		return userErrorf("Only staff with Manage Server, or the form's reviewer role, can decide applications.")
	}
	status := entryAccepted
	if verb == "decline" {
		status = entryDeclined
	}
	reviewer := interactionUser(i).ID
	done, err := appForms.Decide(entryID, status, reviewer, note)
	if err != nil {
		return err
	}
	if !done {
		return userErrorf("Application #%d was already decided by someone else.", entryID)
	}
	if err := deferEphemeral(s, i); err != nil {
		appLogger.Warnf("Could not defer a decision: %v", err)
	}
	outcome := []string{fmt.Sprintf("Application #%d %s.", entryID, status)}
	member := lookupMember(s, i.GuildID, e.UserID)
	if member == nil {
		outcome = append(outcome, "They've left the server, so they weren't told and got no role.")
	} else {
		if status == entryAccepted && f.AcceptRoleID != "" {
			outcome = append(outcome, giveAcceptRole(s, f, member))
		}
		outcome = append(outcome, tellApplicant(s, f, e.UserID, status, note))
	}
	editResponse(s, i, strings.Join(outcome, " "))
	refreshReview(s, f, entryID)
	if e.ChatChannelID != "" {
		mark := "✅ accepted"
		if status == entryDeclined {
			mark = "❌ declined"
		}
		words := fmt.Sprintf("Application #%d was **%s** by <@%s>.", entryID, mark, reviewer)
		if note != "" {
			words += "\n**Note:** " + note
		}
		if _, err := s.ChannelMessageSendComplex(e.ChatChannelID, &discordgo.MessageSend{Content: words, AllowedMentions: &discordgo.MessageAllowedMentions{}}); err != nil {
			appLogger.Debugf("Could not post the decision in chat %s: %v", e.ChatChannelID, err)
		}
	}
	return nil
}

func giveAcceptRole(s *discordgo.Session, f *appForm, member *discordgo.Member) string {
	facts, err := loadRoleFacts(s, f.GuildID)
	if err != nil {
		appLogger.Warnf("Could not check roles for form %d: %v", f.ID, err)
		return "I couldn't check the form's role, so none was given; give it by hand."
	}
	role, ok := facts.Roles[f.AcceptRoleID]
	if !ok {
		return "The form's role no longer exists, so no role was given."
	}
	if why := roleProblem(role, facts); why != "" {
		return fmt.Sprintf("%s wasn't given: %s.", roleMention(role.ID), why)
	}
	if hasRole(member, role.ID) {
		return fmt.Sprintf("They already have %s.", roleMention(role.ID))
	}
	err = s.GuildMemberRoleAdd(f.GuildID, member.User.ID, role.ID, discordgo.WithAuditLogReason("Application to "+f.Name+" accepted"))
	if err != nil {
		appLogger.Warnf("Could not give the role for form %d: %v", f.ID, err)
		return fmt.Sprintf("Discord refused to give %s; give it by hand.", roleMention(role.ID))
	}
	return fmt.Sprintf("Gave them %s.", roleMention(role.ID))
}

func tellApplicant(s *discordgo.Session, f *appForm, userID, status, note string) string {
	good := status == entryAccepted
	if (good && !f.DMAccept) || (!good && !f.DMDecline) {
		verb := "accepts"
		if !good {
			verb = "declines"
		}
		return fmt.Sprintf("This form doesn't DM when it %s, so they weren't sent one; they can check with 🔎 My application.", verb)
	}
	guildName := "the server"
	if g, err := lookupGuild(s, f.GuildID); err == nil {
		guildName = g.Name
	}
	words := fmt.Sprintf("Your application to **%s** in **%s** wasn't accepted this time.", f.Name, guildName)
	color := colorDeclined
	if good {
		words, color = fmt.Sprintf("✅ Your application to **%s** in **%s** was accepted!", f.Name, guildName), colorAccepted
	}
	if note != "" {
		words += "\n\n**A note from staff:** " + note
	}
	dm, err := s.UserChannelCreate(userID)
	if err == nil {
		_, err = s.ChannelMessageSendEmbed(dm.ID, &discordgo.MessageEmbed{Description: words, Color: color})
	}
	if err != nil {
		return "Their DMs are closed, so they weren't told; they can check with 🔎 My application."
	}
	return "They were told by DM."
}
