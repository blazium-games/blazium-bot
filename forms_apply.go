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

// Applying: 📝 Apply opens the form's first pop-up; a longer form has more pages with a
// Continue button between them (a pop-up can't open another pop-up). The answers so far
// are kept in memory for formDraftTTL, keyed by a random token in the pop-up's custom ID.
//
// Custom IDs (never change a format already posted on a message):
//
//	form:<form id>:apply | form:<form id>:status   Apply and My application on a posted form
//	forms:mine                                     My applications on a panel of forms
//	formtry:<form id>                              👀 Try it, from /form show (nothing is saved)
//	formpage:<token>                               one page's pop-up
//	formnext:<token>                               Continue to the next page

const formDraftTTL = 15 * time.Minute

func registerFormRoutes() {
	registerComponentRoute("form", handleFormButton)
	registerComponentRoute("forms", handleMineButton)
	registerComponentRoute("formtry", handleFormTryButton)
	registerComponentRoute("formnext", handleFormNextButton)
	registerComponentRoute("formdel", handleFormDeleteButton)
	registerComponentRoute("formpanel", handleFormPanelMaker)
	registerComponentRoute("application", handleReviewButton)
	registerModalRoute("formpage", handleFormPageSubmit)
	registerModalRoute("formtext", handleFormTextSubmit)
	registerModalRoute("appnote", handleNoteSubmit)
}

func formButton(formID int64, verb string) discordgo.Button {
	if verb == "apply" {
		return discordgo.Button{Label: "📝 Apply", Style: discordgo.SuccessButton, CustomID: fmt.Sprintf("form:%d:apply", formID)}
	}
	return discordgo.Button{Label: "🔎 My application", Style: discordgo.SecondaryButton, CustomID: fmt.Sprintf("form:%d:status", formID)}
}

func mineButton() discordgo.Button {
	return discordgo.Button{Label: "🔎 My applications", Style: discordgo.SecondaryButton, CustomID: "forms:mine"}
}

// ============================================================================
// DRAFTS
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

// ============================================================================
// POP-UPS
// ============================================================================

func questionField(q formQuestion) string { return "q" + strconv.FormatInt(q.ID, 10) }

// buildFormPage is the pop-up for the draft's current page.
func buildFormPage(d *formDraft, token string) *discordgo.InteractionResponseData {
	page := ""
	if d.Total() > 1 {
		page = fmt.Sprintf(" (%d/%d)", d.Page+1, d.Total())
	}
	title := truncateRunes(d.FormName, formNameMax-len([]rune(page))) + page
	if strings.TrimSpace(title) == "" {
		title = "Application"
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
	return &discordgo.InteractionResponseData{CustomID: "formpage:" + token, Title: title, Components: components}
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

func showModal(s *discordgo.Session, i *discordgo.InteractionCreate, data *discordgo.InteractionResponseData) error {
	return s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{Type: discordgo.InteractionResponseModal, Data: data})
}

func interactionUser(i *discordgo.InteractionCreate) *discordgo.User {
	if i.Member != nil && i.Member.User != nil {
		return i.Member.User
	}
	return i.User
}

// ============================================================================
// BUTTONS
// ============================================================================

func formsOff(s *discordgo.Session, i *discordgo.InteractionCreate) bool {
	if appForms == nil {
		respondEphemeral(s, i, "Applications aren't running right now.")
		return true
	}
	if i.GuildID == "" || i.Member == nil {
		respondEphemeral(s, i, "Forms only work inside a server.")
		return true
	}
	return false
}

func handleFormButton(s *discordgo.Session, i *discordgo.InteractionCreate, customID string) {
	parts := strings.Split(customID, ":")
	id, ok := parseID(parts, 1)
	if !ok || len(parts) != 3 || (parts[2] != "apply" && parts[2] != "status") {
		respondEphemeral(s, i, "This button no longer does anything.")
		return
	}
	if formsOff(s, i) {
		return
	}
	var err error
	if parts[2] == "apply" {
		err = startApplying(s, i, id, false)
	} else {
		err = myStatus(s, i, id)
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
	if err := startApplying(s, i, id, true); err != nil {
		replyEphemeral(s, i, errorText("Trying a form", err))
	}
}

// startApplying opens the first page of a form (preview: for staff to try it; nothing is saved or sent).
func startApplying(s *discordgo.Session, i *discordgo.InteractionCreate, formID int64, preview bool) error {
	f, err := needForm(i.GuildID, formID)
	if err != nil {
		return err
	}
	user := interactionUser(i)
	if !preview {
		if !f.Open {
			return userErrorf("**%s** isn't taking applications right now.", f.Name)
		}
		last, err := appForms.LatestEntry(formID, user.ID)
		if err != nil {
			return err
		}
		if last != nil && last.Status == entryPending {
			return userErrorf("You've already applied to **%s**. It's waiting for a decision; press 🔎 My application to check on it.", f.Name)
		}
	}
	questions, err := appForms.Questions(formID)
	if err != nil {
		return err
	}
	if len(questions) == 0 {
		if f.ChatCategoryID == "" {
			return userErrorf("This form has no questions yet. Staff can add some with `/form question add`.")
		}
		if preview {
			respondEphemeral(s, i, "👀 This form has no questions: pressing it opens a private chat with staff straight away.")
			return nil
		}
		// A ticket: straight to the chat.
		return submitApplication(s, i, f, &formDraft{FormID: formID, GuildID: i.GuildID, UserID: user.ID, FormName: f.Name})
	}
	d := &formDraft{FormID: formID, GuildID: i.GuildID, UserID: user.ID, FormName: f.Name, Questions: questions, Header: f.Header, Preview: preview}
	token := saveDraft(d)
	if err := showModal(s, i, buildFormPage(d, token)); err != nil {
		dropDraft(token)
		return err
	}
	return nil
}

func handleFormNextButton(s *discordgo.Session, i *discordgo.InteractionCreate, customID string) {
	token := strings.TrimPrefix(customID, "formnext:")
	d := takeDraft(token, interactionUser(i).ID)
	if d == nil || d.Done() {
		respondEphemeral(s, i, fmt.Sprintf("This form has expired (you have %d minutes between pages). Please press 📝 Apply again.", int(formDraftTTL.Minutes())))
		return
	}
	if err := showModal(s, i, buildFormPage(d, token)); err != nil {
		appLogger.Warnf("Could not open the next form page: %v", err)
	}
}

func handleFormPageSubmit(s *discordgo.Session, i *discordgo.InteractionCreate, customID string) {
	token := strings.TrimPrefix(customID, "formpage:")
	d := takeDraft(token, interactionUser(i).ID)
	if d == nil || d.Done() {
		respondEphemeral(s, i, "This form has expired. Please press 📝 Apply again.")
		return
	}
	if appForms == nil {
		respondEphemeral(s, i, "Applications aren't running right now.")
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
	f, err := needForm(d.GuildID, d.FormID)
	if err == nil {
		err = submitApplication(s, i, f, d)
	}
	if err != nil {
		replyEphemeral(s, i, errorText("Sending an application", err))
	}
}

// submitApplication saves the application, tells the member, opens their chat if the form
// has chats, and posts it for review.
func submitApplication(s *discordgo.Session, i *discordgo.InteractionCreate, f *appForm, d *formDraft) error {
	if !f.Open {
		return userErrorf("Sorry, **%s** stopped taking applications while you were filling it in.", f.Name)
	}
	entryID, err := appForms.Submit(&appEntry{FormID: f.ID, GuildID: i.GuildID, UserID: d.UserID, Answers: dumpAnswers(d.Saved())})
	if err == errAlreadyExists {
		return userErrorf("You've already sent an application to this form.")
	}
	if err != nil {
		return err
	}
	chats := f.ChatCategoryID != ""
	text := fmt.Sprintf("✅ Opening a private chat with staff for **%s**…", f.Name)
	if len(d.Questions) > 0 {
		text = fmt.Sprintf("✅ Your application to **%s** is sent. Staff will look at it, and I'll DM you the decision (if your DMs are open). Press 🔎 My application on the form to check on it.", f.Name)
		if chats {
			text += "\nOpening a private chat with staff for you…"
		}
	}
	respondEphemeral(s, i, text)
	chatID := ""
	if chats {
		var problem string
		chatID, problem = openApplicantChat(s, f, entryID, i.Member)
		words := fmt.Sprintf("💬 Your private chat with staff is <#%s>. Your application is posted there to talk it over.", chatID)
		if chatID == "" {
			words = fmt.Sprintf("I couldn't open a private chat (%s); staff can still reach you.", problem)
		}
		_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{Content: words, Flags: discordgo.MessageFlagsEphemeral})
	}
	postForReview(s, f, entryID)
	if chatID != "" {
		postInChat(s, f, entryID, chatID)
	}
	return nil
}

func myStatus(s *discordgo.Session, i *discordgo.InteractionCreate, formID int64) error {
	f, err := needForm(i.GuildID, formID)
	if err != nil {
		return err
	}
	last, err := appForms.LatestEntry(formID, interactionUser(i).ID)
	if err != nil {
		return err
	}
	var text string
	if last == nil {
		text = fmt.Sprintf("You haven't applied to **%s** yet. Press 📝 Apply to start.", f.Name)
	} else {
		text = fmt.Sprintf("Your application to **%s** (#%d): %s.", f.Name, last.ID, entryStatusWords[last.Status])
		if last.Status != entryPending && last.Note != "" {
			text += "\nA note from staff: " + last.Note
		}
		if last.Status != entryPending && f.Open {
			text += "\nYou can apply again with 📝 Apply."
		}
	}
	respondEphemeral(s, i, text)
	return nil
}

func handleMineButton(s *discordgo.Session, i *discordgo.InteractionCreate, customID string) {
	if customID != "forms:mine" {
		respondEphemeral(s, i, "This button no longer does anything.")
		return
	}
	if formsOff(s, i) {
		return
	}
	forms, err := appForms.ListForms(i.GuildID)
	if err != nil {
		respondEphemeral(s, i, errorText("My applications", err))
		return
	}
	var lines []string
	for _, f := range forms {
		last, err := appForms.LatestEntry(f.ID, interactionUser(i).ID)
		if err != nil {
			respondEphemeral(s, i, errorText("My applications", err))
			return
		}
		if last == nil {
			continue
		}
		line := fmt.Sprintf("• **%s** (#%d): %s", f.Name, last.ID, entryStatusWords[last.Status])
		if last.Status != entryPending && last.Note != "" {
			line += ". A note from staff: " + last.Note
		}
		lines = append(lines, line)
	}
	text := strings.Join(lines, "\n")
	if text == "" {
		text = "You haven't sent anything to these forms yet. Press one above to start."
	}
	respondEphemeral(s, i, truncateRunes(text, 1900))
}
