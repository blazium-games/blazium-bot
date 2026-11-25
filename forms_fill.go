package main

import (
	"fmt"
	"strings"

	"github.com/bwmarrin/discordgo"
)

// Filling in a form and reviewing submissions.
//
// Custom IDs (never change a format already posted on a message):
//
//	form:<form id>:fill | form:<form id>:mine           a form's button, and My submissions
//	formtry:<form id>                                   👀 Try it, from /form show (nothing is saved)
//	formpage:<token> | formnext:<token>                 a page's pop-up, and Continue to the next page
//	formsub:<sub id>:accept|decline|yes|no|unsure       review buttons on a submission (forms with review)
//	formnote:<sub id>:accept|decline                    the reviewer's note pop-up

func formButtonLabel(f *appForm) string {
	if f.ButtonLabel != "" {
		return f.ButtonLabel
	}
	return "📝 Fill in"
}

// formFillButton is a form's button. On a panel it's named after the form.
func formFillButton(f appForm, onPanel bool) discordgo.Button {
	b := discordgo.Button{Label: truncateRunes(formButtonLabel(&f), 80), Style: discordgo.SuccessButton, CustomID: fmt.Sprintf("form:%d:fill", f.ID)}
	if onPanel {
		b.Label, b.Style = truncateRunes(f.Name, 80), discordgo.PrimaryButton
	}
	if e, err := parseButtonEmoji(f.Emoji); err == nil && e != nil {
		b.Emoji = e
	}
	return b
}

func formMineButton(formID int64) discordgo.Button {
	return discordgo.Button{Label: "🔎 My submissions", Style: discordgo.SecondaryButton, CustomID: fmt.Sprintf("form:%d:mine", formID)}
}

// ============================================================================
// FILLING IN
// ============================================================================

func handleFormButton(s *discordgo.Session, i *discordgo.InteractionCreate, customID string) {
	parts := strings.Split(customID, ":")
	id, ok := parseID(parts, 1)
	if !ok || len(parts) != 3 || (parts[2] != "fill" && parts[2] != "mine") {
		respondEphemeral(s, i, "This button no longer does anything.")
		return
	}
	if formsOff(s, i) {
		return
	}
	var err error
	if parts[2] == "fill" {
		err = startForm(s, i, id, false)
	} else {
		var text string
		if text, err = mySubmissions(i, id); err == nil {
			respondEphemeral(s, i, text)
		}
	}
	if err != nil {
		replyEphemeral(s, i, errorText("Form button", err))
	}
}

func handleFormTryButton(s *discordgo.Session, i *discordgo.InteractionCreate, customID string) {
	id, ok := parseID(strings.Split(customID, ":"), 1)
	if !ok {
		respondEphemeral(s, i, "This button no longer does anything.")
		return
	}
	if formsOff(s, i) {
		return
	}
	if err := startForm(s, i, id, true); err != nil {
		replyEphemeral(s, i, errorText("Trying a form", err))
	}
}

// canSubmit is why this member can't send this form now, if they can't.
func canSubmit(f *appForm, userID string) error {
	if !f.Open {
		return userErrorf("**%s** isn't taking submissions right now.", f.Name)
	}
	if f.MaxPerMember <= 0 {
		return nil
	}
	subs, err := appForms.SubmissionsOf(f.ID, userID)
	if err != nil {
		return err
	}
	counted := 0
	for _, sub := range subs {
		if sub.Status != subDeclined {
			counted++
		}
	}
	if counted >= f.MaxPerMember {
		if f.MaxPerMember == 1 {
			return userErrorf("You've already sent **%s**. Press 🔎 My submissions to see it.", f.Name)
		}
		return userErrorf("You've already sent **%s** %d times, which is the most allowed.", f.Name, f.MaxPerMember)
	}
	return nil
}

func startForm(s *discordgo.Session, i *discordgo.InteractionCreate, formID int64, preview bool) error {
	f, err := needForm(i.GuildID, formID)
	if err != nil {
		return err
	}
	user := interactionUser(i)
	if !preview {
		if err := canSubmit(f, user.ID); err != nil {
			return err
		}
	}
	questions, err := appForms.Questions(formID)
	if err != nil {
		return err
	}
	if len(questions) == 0 {
		return userErrorf("This form has no questions yet. Staff can add some with `/form question add`.")
	}
	d := &formDraft{FormID: formID, GuildID: i.GuildID, UserID: user.ID, FormName: f.Name, Questions: questions, Header: f.Header, Preview: preview}
	token := saveDraft(d)
	if err := showModal(s, i, buildIntakePage(d, "formpage:"+token)); err != nil {
		dropDraft(token)
		return err
	}
	return nil
}

func handleFormNextButton(s *discordgo.Session, i *discordgo.InteractionCreate, customID string) {
	token := strings.TrimPrefix(customID, "formnext:")
	d := takeDraft(token, interactionUser(i).ID)
	if d == nil || d.Done() {
		respondEphemeral(s, i, fmt.Sprintf("This form has expired (you have %d minutes between pages). Please press its button again.", int(formDraftTTL.Minutes())))
		return
	}
	if err := showModal(s, i, buildIntakePage(d, "formpage:"+token)); err != nil {
		appLogger.Warnf("Could not open the next form page: %v", err)
	}
}

func handleFormPageSubmit(s *discordgo.Session, i *discordgo.InteractionCreate, customID string) {
	token := strings.TrimPrefix(customID, "formpage:")
	d := takeDraft(token, interactionUser(i).ID)
	if d == nil || d.Done() {
		respondEphemeral(s, i, "This form has expired. Please press its button again.")
		return
	}
	if formsOff(s, i) {
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
					discordgo.Button{Label: fmt.Sprintf("Continue to page %d of %d ▶", d.Page+1, d.Total()), Style: discordgo.PrimaryButton, CustomID: "formnext:" + token},
				}}},
			},
		})
		if err != nil {
			appLogger.Warnf("Could not answer a form page: %v", err)
		}
		return
	}
	dropDraft(token)
	if d.Preview {
		text := formTranscript("Preview of "+d.FormName, "Nothing was saved or sent.", d.Saved())
		respondEphemeral(s, i, "👀 That's how the form works. Nothing was sent.\n```\n"+truncateRunes(strings.ReplaceAll(text, "```", "'''"), 1800)+"\n```")
		return
	}
	if err := deferEphemeral(s, i); err != nil {
		appLogger.Warnf("Could not defer a form submit: %v", err)
		return
	}
	msg, err := submitForm(s, i, d)
	if err != nil {
		msg = errorText("Sending a form", err)
	}
	editResponse(s, i, msg)
}

// submitForm saves the submission, posts it in the form's channel and sends the member their receipt.
func submitForm(s *discordgo.Session, i *discordgo.InteractionCreate, d *formDraft) (string, error) {
	f, err := needForm(d.GuildID, d.FormID)
	if err != nil {
		return "", err
	}
	if err := canSubmit(f, d.UserID); err != nil { // again: it may have closed while they typed
		return "", err
	}
	status := subReceived
	if f.Review {
		status = subPending
	}
	id, err := appForms.Submit(&submission{FormID: f.ID, GuildID: d.GuildID, UserID: d.UserID, Answers: dumpAnswers(d.Saved()), Status: status})
	if err != nil {
		return "", err
	}
	sub, err := appForms.GetSubmission(d.GuildID, id)
	if err != nil || sub == nil {
		return "", fmt.Errorf("reloading submission %d: %v", id, err)
	}
	member := i.Member
	if msg, err := s.ChannelMessageSendComplex(f.ChannelID, submissionMessage(f, sub, member, nil)); err != nil {
		appLogger.Errorf("Submission %d: could not post it in %s: %v", id, f.ChannelID, err)
	} else if err := appForms.SetSubmissionMessage(id, msg.ChannelID, msg.ID); err != nil {
		appLogger.Warnf("Submission %d: could not save its message: %v", id, err)
	}
	words := fmt.Sprintf("✅ Sent! Your **%s** submission is #%d.", f.Name, id)
	if f.Review {
		words += " Staff will look at it, and you can check on it with 🔎 My submissions."
	}
	if f.DMReceipt {
		receipt := submissionMessage(f, sub, member, nil)
		receipt.Embeds[0].Title = truncateRunes(fmt.Sprintf("📨 Your %s submission (#%d)", f.Name, id), 256)
		if g, err := lookupGuild(s, f.GuildID); err == nil {
			receipt.Embeds[0].Footer = &discordgo.MessageEmbedFooter{Text: "Sent in " + g.Name}
		}
		receipt.Components = nil
		dm, err := s.UserChannelCreate(d.UserID)
		if err == nil {
			_, err = s.ChannelMessageSendComplex(dm.ID, receipt)
		}
		if err == nil {
			words += " I've DMed you a copy."
		}
	}
	return words, nil
}

func mySubmissions(i *discordgo.InteractionCreate, formID int64) (string, error) {
	f, err := needForm(i.GuildID, formID)
	if err != nil {
		return "", err
	}
	subs, err := appForms.SubmissionsOf(formID, interactionUser(i).ID)
	if err != nil {
		return "", err
	}
	if len(subs) == 0 {
		return fmt.Sprintf("You haven't sent **%s** yet. Press its button to start.", f.Name), nil
	}
	var lines []string
	for _, sub := range subs {
		line := fmt.Sprintf("• #%d, <t:%d:R>: %s", sub.ID, sub.SubmittedAt.Unix(), subStatusWords[sub.Status])
		if sub.Note != "" && (sub.Status == subAccepted || sub.Status == subDeclined) {
			line += ". A note from staff: " + sub.Note
		}
		lines = append(lines, line)
	}
	return fitLines(fmt.Sprintf("Your **%s** submissions:", f.Name), lines, func(left int) string { return fmt.Sprintf("…and %d older.", left) }), nil
}

// ============================================================================
// THE SUBMISSION MESSAGE
// ============================================================================

var voteVerbs = []struct{ Key, Label string }{{"yes", "👍 Yes"}, {"no", "👎 No"}, {"unsure", "🤷 Unsure"}}

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

// submissionMessage is a submission as staff see it: who sent it, the answers, and (with
// review) its status, the votes and the buttons.
func submissionMessage(f *appForm, sub *submission, member *discordgo.Member, votes []appVote) *discordgo.MessageSend {
	who := "<@" + sub.UserID + ">"
	if member != nil && member.User != nil {
		who += " (" + member.User.Username + ")"
	}
	lines := []string{"**From:** " + who, fmt.Sprintf("**Sent:** <t:%d:f>", sub.SubmittedAt.Unix())}
	if member != nil && !member.JoinedAt.IsZero() {
		lines = append(lines, fmt.Sprintf("**Joined the server:** <t:%d:R>", member.JoinedAt.Unix()))
	}
	color := formColor
	if f.Review || sub.Status != subReceived {
		lines = append(lines, "**Status:** "+subStatusWords[sub.Status])
		switch sub.Status {
		case subAccepted:
			color = colorOpened
		case subDeclined:
			color = colorClosed
		}
		if sub.DecidedBy != "" {
			lines = append(lines, "**Decided by:** <@"+sub.DecidedBy+">")
			if sub.Note != "" {
				lines = append(lines, "**Note:** "+sub.Note)
			}
		}
	}
	embed := &discordgo.MessageEmbed{
		Title:       truncateRunes(fmt.Sprintf("📝 %s: submission #%d", f.Name, sub.ID), 256),
		Description: strings.Join(lines, "\n"),
		Color:       color,
	}
	files := answerFields(embed, loadAnswers(sub.Answers), fmt.Sprintf("%s submission #%d", f.Name, sub.ID), sub.UserID)
	msg := &discordgo.MessageSend{Embeds: []*discordgo.MessageEmbed{embed}, Files: files, AllowedMentions: &discordgo.MessageAllowedMentions{}}
	if sub.Status != subPending {
		return msg
	}
	embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{Name: "🗳️ Staff votes", Value: voteTally(votes, true)})
	decide := discordgo.ActionsRow{Components: []discordgo.MessageComponent{
		discordgo.Button{Label: "✅ Accept", Style: discordgo.SuccessButton, CustomID: fmt.Sprintf("formsub:%d:accept", sub.ID)},
		discordgo.Button{Label: "❌ Decline", Style: discordgo.DangerButton, CustomID: fmt.Sprintf("formsub:%d:decline", sub.ID)},
	}}
	vote := discordgo.ActionsRow{}
	for _, v := range voteVerbs {
		count := 0
		for _, cast := range votes {
			if cast.Vote == v.Key {
				count++
			}
		}
		vote.Components = append(vote.Components, discordgo.Button{
			Label: fmt.Sprintf("%s · %d", v.Label, count), Style: discordgo.SecondaryButton, CustomID: fmt.Sprintf("formsub:%d:%s", sub.ID, v.Key),
		})
	}
	msg.Components = []discordgo.MessageComponent{decide, vote}
	return msg
}

// refreshSubmission redraws the posted copy of a submission.
func refreshSubmission(s *discordgo.Session, f *appForm, subID int64) {
	sub, err := appForms.GetSubmission(f.GuildID, subID)
	if err != nil || sub == nil || sub.MessageID == "" {
		return
	}
	votes, _ := appForms.Votes(subID)
	msg := submissionMessage(f, sub, lookupMember(s, f.GuildID, sub.UserID), votes)
	components := msg.Components
	if components == nil {
		components = []discordgo.MessageComponent{}
	}
	_, err = s.ChannelMessageEditComplex(&discordgo.MessageEdit{ID: sub.MessageID, Channel: sub.ChannelID, Embeds: &msg.Embeds, Components: &components})
	if err != nil {
		appLogger.Debugf("Submission %d: message not updated: %v", subID, err)
	}
}

// ============================================================================
// REVIEW
// ============================================================================

// canReviewForm: Manage Server (or the staff role), or the form's reviewer role.
func canReviewForm(i *discordgo.InteractionCreate, f *appForm) bool {
	return canManageServer(i) || hasRole(i.Member, f.ReviewerRoleID)
}

func submissionAndForm(guildID string, id int64) (*submission, *appForm, error) {
	sub, err := appForms.GetSubmission(guildID, id)
	if err != nil {
		return nil, nil, err
	}
	if sub == nil {
		return nil, nil, userErrorf("I can't find that submission any more.")
	}
	f, err := needForm(guildID, sub.FormID)
	if err != nil {
		return nil, nil, err
	}
	return sub, f, nil
}

func handleSubmissionButton(s *discordgo.Session, i *discordgo.InteractionCreate, customID string) {
	parts := strings.Split(customID, ":")
	id, ok := parseID(parts, 1)
	if !ok || len(parts) != 3 {
		respondEphemeral(s, i, "This button no longer does anything.")
		return
	}
	if formsOff(s, i) {
		return
	}
	if err := submissionButton(s, i, id, parts[2]); err != nil {
		replyEphemeral(s, i, errorText("Review button", err))
	}
}

func submissionButton(s *discordgo.Session, i *discordgo.InteractionCreate, id int64, verb string) error {
	sub, f, err := submissionAndForm(i.GuildID, id)
	if err != nil {
		return err
	}
	if !canReviewForm(i, f) {
		return userErrorf("Only staff with Manage Server, or the form's reviewer role, can review submissions.")
	}
	if sub.Status != subPending {
		return userErrorf("Submission #%d was already %s.", id, sub.Status)
	}
	switch verb {
	case "accept", "decline":
		title := "Accept submission"
		if verb == "decline" {
			title = "Decline submission"
		}
		required := false
		return showModal(s, i, &discordgo.InteractionResponseData{
			CustomID: fmt.Sprintf("formnote:%d:%s", id, verb),
			Title:    title,
			Components: []discordgo.MessageComponent{discordgo.Label{
				Label: "A note for the member (optional)", Description: "Sent to them with the decision.",
				Component: discordgo.TextInput{CustomID: "note", Style: discordgo.TextInputParagraph, Required: &required, MaxLength: formReasonMax},
			}},
		})
	case "yes", "no", "unsure":
		return castVote(s, i, sub, f, verb)
	}
	respondEphemeral(s, i, "This button no longer does anything.")
	return nil
}

// castVote: press a vote to cast or change it, press the same one again to take it back.
func castVote(s *discordgo.Session, i *discordgo.InteractionCreate, sub *submission, f *appForm, verb string) error {
	votes, err := appForms.Votes(sub.ID)
	if err != nil {
		return err
	}
	userID := interactionUser(i).ID
	next := verb
	for _, v := range votes {
		if v.UserID == userID && v.Vote == verb {
			next = ""
		}
	}
	if err := appForms.SetVote(sub.ID, userID, next); err != nil {
		return err
	}
	if votes, err = appForms.Votes(sub.ID); err != nil {
		return err
	}
	if i.Message != nil && i.Message.ID == sub.MessageID {
		msg := submissionMessage(f, sub, lookupMember(s, f.GuildID, sub.UserID), votes)
		return s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseUpdateMessage,
			Data: &discordgo.InteractionResponseData{Embeds: msg.Embeds, Components: msg.Components, AllowedMentions: &discordgo.MessageAllowedMentions{}},
		})
	}
	words := "Vote taken back."
	if next != "" {
		words = "Vote saved."
	}
	respondEphemeral(s, i, words+" Now "+voteTally(votes, false)+".")
	refreshSubmission(s, f, sub.ID)
	return nil
}

func handleFormNoteSubmit(s *discordgo.Session, i *discordgo.InteractionCreate, customID string) {
	parts := strings.Split(customID, ":")
	id, ok := parseID(parts, 1)
	if !ok || len(parts) != 3 || (parts[2] != "accept" && parts[2] != "decline") {
		respondEphemeral(s, i, "That pop-up has expired. Please try again.")
		return
	}
	if formsOff(s, i) {
		return
	}
	sub, f, err := submissionAndForm(i.GuildID, id)
	if err == nil && !canReviewForm(i, f) {
		err = userErrorf("Only staff with Manage Server, or the form's reviewer role, can review submissions.")
	}
	if err != nil {
		respondEphemeral(s, i, errorText("Deciding a submission", err))
		return
	}
	status := subAccepted
	if parts[2] == "decline" {
		status = subDeclined
	}
	note := strings.TrimSpace(modalValues(i)["note"].text())
	reviewer := interactionUser(i).ID
	done, err := appForms.Decide(id, status, reviewer, note)
	if err != nil || !done {
		if err == nil {
			err = userErrorf("Submission #%d was already decided by someone else.", id)
		}
		respondEphemeral(s, i, errorText("Deciding a submission", err))
		return
	}
	if err := deferEphemeral(s, i); err != nil {
		appLogger.Warnf("Could not defer a decision: %v", err)
	}
	outcome := []string{fmt.Sprintf("Submission #%d %s.", id, status)}
	member := lookupMember(s, i.GuildID, sub.UserID)
	if member == nil {
		outcome = append(outcome, "They've left the server, so they weren't told and got no role.")
	} else {
		if status == subAccepted && f.AcceptRoleID != "" {
			outcome = append(outcome, giveAcceptRole(s, f, member))
		}
		outcome = append(outcome, tellDecision(s, f, sub, status, note))
	}
	editResponse(s, i, strings.Join(outcome, " "))
	refreshSubmission(s, f, id)
}

func giveAcceptRole(s *discordgo.Session, f *appForm, member *discordgo.Member) string {
	facts, err := loadRoleFacts(s, f.GuildID)
	if err != nil {
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
	if err := s.GuildMemberRoleAdd(f.GuildID, member.User.ID, role.ID, discordgo.WithAuditLogReason("Submission to "+f.Name+" accepted")); err != nil {
		return fmt.Sprintf("Discord refused to give %s; give it by hand.", roleMention(role.ID))
	}
	return fmt.Sprintf("Gave them %s.", roleMention(role.ID))
}

func tellDecision(s *discordgo.Session, f *appForm, sub *submission, status, note string) string {
	good := status == subAccepted
	if (good && !f.DMAccept) || (!good && !f.DMDecline) {
		return "This form doesn't DM about that decision; they can check with 🔎 My submissions."
	}
	guildName := "the server"
	if g, err := lookupGuild(s, f.GuildID); err == nil {
		guildName = g.Name
	}
	words := fmt.Sprintf("Your **%s** submission (#%d) in **%s** wasn't accepted this time.", f.Name, sub.ID, guildName)
	color := colorClosed
	if good {
		words, color = fmt.Sprintf("✅ Your **%s** submission (#%d) in **%s** was accepted!", f.Name, sub.ID, guildName), colorOpened
	}
	if note != "" {
		words += "\n\n**A note from staff:** " + note
	}
	dm, err := s.UserChannelCreate(sub.UserID)
	if err == nil {
		_, err = s.ChannelMessageSendEmbed(dm.ID, &discordgo.MessageEmbed{Description: words, Color: color})
	}
	if err != nil {
		return "Their DMs are closed, so they weren't told; they can check with 🔎 My submissions."
	}
	return "They were told by DM."
}

// ============================================================================
// /form submissions and /form view
// ============================================================================

func submissionList(i *discordgo.InteractionCreate, opts cmdOpts) (string, error) {
	formID, _ := optInt(opts, "form")
	status, _ := optString(opts, "status")
	forms, err := appForms.ListForms(i.GuildID)
	if err != nil {
		return "", err
	}
	names := map[int64]string{}
	for _, f := range forms {
		names[f.ID] = f.Name
	}
	found, err := appForms.ListSubmissions(i.GuildID, formID, status)
	if err != nil {
		return "", err
	}
	if len(found) == 0 {
		return "No submissions yet.", nil
	}
	var lines []string
	for _, sub := range found {
		line := fmt.Sprintf("#%d · %s · <@%s> · <t:%d:R> · %s", sub.ID, names[sub.FormID], sub.UserID, sub.SubmittedAt.Unix(), subStatusWords[sub.Status])
		if sub.MessageID != "" {
			line += " · [open](" + messageLink(i.GuildID, sub.ChannelID, sub.MessageID) + ")"
		}
		lines = append(lines, line)
	}
	return fitLines(fmt.Sprintf("**%d submission(s), newest first:**", len(found)), lines, func(left int) string {
		return fmt.Sprintf("…and %d more. `/form view` opens any by number.", left)
	}), nil
}

func submissionView(s *discordgo.Session, i *discordgo.InteractionCreate, opts cmdOpts) {
	number, _ := optInt(opts, "number")
	sub, f, err := submissionAndForm(i.GuildID, number)
	if err != nil {
		respondEphemeral(s, i, errorText("/form view", err))
		return
	}
	votes, _ := appForms.Votes(sub.ID)
	msg := submissionMessage(f, sub, lookupMember(s, i.GuildID, sub.UserID), votes)
	err = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Embeds: msg.Embeds, Components: msg.Components, Files: msg.Files,
			Flags: discordgo.MessageFlagsEphemeral, AllowedMentions: &discordgo.MessageAllowedMentions{},
		},
	})
	if err != nil {
		appLogger.Warnf("Could not answer /form view: %v", err)
	}
}
