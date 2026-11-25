package main

import (
	"fmt"
	"strings"

	"github.com/bwmarrin/discordgo"
)

// /tickettype: setting up ticket types, their staff roles, intake questions and buttons.

const (
	guildChannelLimit    = 500 // Discord's limits
	categoryChannelLimit = 50
)

func handleTicketTypeCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if ticketsOff(s, i) {
		return
	}
	if !canManageServer(i) {
		respondEphemeral(s, i, "You need Manage Server (or the staff role) to set up ticket types.")
		return
	}
	name, opts := commandOptions(i)
	// These answer with a pop-up or an interactive message, so they can't be deferred.
	switch name {
	case "header", "welcome":
		typeTextBox(s, i, name, opts)
		return
	case "delete":
		typeDeleteConfirm(s, i, opts)
		return
	case "show":
		typeShow(s, i, opts)
		return
	}
	if err := deferEphemeral(s, i); err != nil {
		appLogger.Warnf("Could not defer /tickettype: %v", err)
		return
	}
	msg, err := runTicketTypeSubcommand(s, i, name, opts)
	if err != nil {
		msg = errorText("/tickettype "+name, err)
	}
	editResponse(s, i, msg)
}

func runTicketTypeSubcommand(s *discordgo.Session, i *discordgo.InteractionCreate, name string, opts cmdOpts) (string, error) {
	switch name {
	case "create":
		return typeCreate(s, i, opts)
	case "list":
		return typeList(i)
	}
	id, _ := optInt(opts, "type")
	tt, err := needType(i.GuildID, id)
	if err != nil {
		return "", err
	}
	switch name {
	case "edit":
		return typeEdit(s, i, opts, tt)
	case "staff":
		return typeStaff(s, i, opts, tt)
	case "post":
		return typePost(s, i, opts, tt)
	case "question add":
		return questionAdd(opts, tt)
	case "question remove":
		return questionRemove(opts, tt)
	case "question move":
		return questionMove(opts, tt)
	}
	return "", userErrorf("Unknown subcommand.")
}

func typeCreate(s *discordgo.Session, i *discordgo.InteractionCreate, opts cmdOpts) (string, error) {
	name, _ := optString(opts, "name")
	name = strings.TrimSpace(name)
	if name == "" {
		return "", userErrorf("The ticket type needs a name.")
	}
	category := optChannel(i, opts, "category")
	if err := checkTicketCategory(s, i.GuildID, category.ID); err != nil {
		return "", err
	}
	logChannel := optChannel(i, opts, "log_channel")
	allowPublic, _ := optBool(opts, "allow_public")
	if err := checkLogChannel(s, i.GuildID, logChannel.ID, allowPublic); err != nil {
		return "", err
	}
	staff := optRole(i, opts, "staff_role")
	if err := checkStaffRole(i.GuildID, staff); err != nil {
		return "", err
	}
	intro, _ := optString(opts, "intro")
	emojiText, _ := optString(opts, "emoji")
	emoji, err := buttonEmojiText(emojiText)
	if err != nil {
		return "", err
	}
	tt := &ticketType{
		GuildID: i.GuildID, Name: name, Intro: strings.TrimSpace(intro), CategoryID: category.ID, LogChannelID: logChannel.ID,
		Open: true, Emoji: emoji, MaxOpen: 1, DMTranscript: true, CreatedBy: i.Member.User.ID,
	}
	id, err := tickets.CreateType(tt)
	if err != nil {
		return "", err
	}
	if err := tickets.AddStaffRole(id, staff.ID); err != nil {
		return "", err
	}
	return fmt.Sprintf("✅ Made the ticket type **%s** (#%d). Tickets open in <#%s>, handled by %s, logged in <#%s>.\nNext:\n"+
		"1. Optionally add intake questions with `/tickettype question add type:%s`.\n"+
		"2. Post its button with `/tickettype post`, or several types on one message with `/ticket panel`.",
		name, id, category.ID, roleMention(staff.ID), logChannel.ID, name), nil
}

func typeEdit(s *discordgo.Session, i *discordgo.InteractionCreate, opts cmdOpts, tt *ticketType) (string, error) {
	var changed []string
	if v, ok := optString(opts, "name"); ok && strings.TrimSpace(v) != "" {
		tt.Name, changed = strings.TrimSpace(v), append(changed, "name")
	}
	if v, ok := optString(opts, "intro"); ok && strings.TrimSpace(v) != "" {
		tt.Intro, changed = strings.TrimSpace(v), append(changed, "intro")
	}
	if ch := optChannel(i, opts, "category"); ch != nil {
		if err := checkTicketCategory(s, i.GuildID, ch.ID); err != nil {
			return "", err
		}
		tt.CategoryID, changed = ch.ID, append(changed, "category")
	}
	if ch := optChannel(i, opts, "log_channel"); ch != nil {
		allowPublic, _ := optBool(opts, "allow_public")
		if err := checkLogChannel(s, i.GuildID, ch.ID, allowPublic); err != nil {
			return "", err
		}
		tt.LogChannelID, changed = ch.ID, append(changed, "log channel")
	}
	if v, ok := optBool(opts, "open"); ok {
		tt.Open, changed = v, append(changed, "open")
	}
	if v, ok := optInt(opts, "max_open"); ok {
		tt.MaxOpen, changed = int(v), append(changed, "max open")
	}
	if v, ok := optBool(opts, "dm_transcript"); ok {
		tt.DMTranscript, changed = v, append(changed, "DM transcript")
	}
	if v, ok := optBool(opts, "ping_staff"); ok {
		tt.PingStaff, changed = v, append(changed, "ping staff")
	}
	if text, ok := optString(opts, "emoji"); ok {
		emoji, err := buttonEmojiText(text)
		if err != nil {
			return "", err
		}
		tt.Emoji, changed = emoji, append(changed, "emoji")
	}
	if len(changed) == 0 {
		return "", userErrorf("Fill in at least one thing to change.")
	}
	if err := tickets.UpdateType(tt); err != nil {
		return "", err
	}
	return fmt.Sprintf("✅ Changed **%s**: %s. Posted buttons keep working; button labels change when you post them again.", tt.Name, strings.Join(changed, ", ")), nil
}

func typeStaff(s *discordgo.Session, i *discordgo.InteractionCreate, opts cmdOpts, tt *ticketType) (string, error) {
	add := optRole(i, opts, "add_role")
	remove := optRole(i, opts, "remove_role")
	if add == nil && remove == nil {
		return "", userErrorf("Pick a role to add or remove.")
	}
	if add != nil {
		if err := checkStaffRole(i.GuildID, add); err != nil {
			return "", err
		}
		if err := tickets.AddStaffRole(tt.ID, add.ID); err != nil {
			return "", err
		}
	}
	if remove != nil {
		if err := tickets.RemoveStaffRole(tt.ID, remove.ID); err != nil {
			return "", err
		}
	}
	staff, err := ticketStaffRoles(s, tt)
	if err != nil {
		return "", err
	}
	if len(staff) == 0 {
		return fmt.Sprintf("⚠️ **%s** has no staff roles now, so no new tickets can open. Add one with `add_role:`.", tt.Name), nil
	}
	return fmt.Sprintf("✅ New **%s** tickets are handled by %s. Tickets already open keep the access they had.", tt.Name, mentionAll(staff)), nil
}

// ticketStaffRoles is a type's staff roles that still exist (never @everyone).
func ticketStaffRoles(s *discordgo.Session, tt *ticketType) ([]string, error) {
	ids, err := tickets.StaffRoles(tt.ID)
	if err != nil {
		return nil, err
	}
	existing := map[string]bool{}
	if g, err := lookupGuild(s, tt.GuildID); err == nil {
		for _, r := range g.Roles {
			existing[r.ID] = true
		}
	}
	var out []string
	for _, id := range ids {
		if id != tt.GuildID && (len(existing) == 0 || existing[id]) {
			out = append(out, id)
		}
	}
	return out, nil
}

func typeList(i *discordgo.InteractionCreate) (string, error) {
	types, err := tickets.ListTypes(i.GuildID)
	if err != nil {
		return "", err
	}
	if len(types) == 0 {
		return "No ticket types yet. Make one with `/tickettype create`.", nil
	}
	var lines []string
	for _, tt := range types {
		open, err := tickets.ListTickets(i.GuildID, tt.ID, ticketOpen)
		if err != nil {
			return "", err
		}
		questions, err := tickets.Questions(tt.ID)
		if err != nil {
			return "", err
		}
		state := "🔴 closed to new tickets"
		if tt.Open {
			state = "🟢 open"
		}
		lines = append(lines, fmt.Sprintf("%s**%s** (#%d) · %s · %d open ticket(s) · %d question(s) · in <#%s>",
			emojiPrefix(tt.Emoji), tt.Name, tt.ID, state, len(open), len(questions), tt.CategoryID))
	}
	return fitLines(lines[0], lines[1:], func(left int) string {
		return fmt.Sprintf("…and %d more. `/tickettype show` opens any type by name.", left)
	}), nil
}

func emojiPrefix(emoji string) string {
	if emoji == "" {
		return ""
	}
	return emoji + " "
}

func typeShow(s *discordgo.Session, i *discordgo.InteractionCreate, opts cmdOpts) {
	id, _ := optInt(opts, "type")
	embed, hasQuestions, err := typeShowEmbed(s, i.GuildID, id)
	if err != nil {
		respondEphemeral(s, i, errorText("/tickettype show", err))
		return
	}
	data := &discordgo.InteractionResponseData{
		Embeds: []*discordgo.MessageEmbed{embed}, Flags: discordgo.MessageFlagsEphemeral,
		AllowedMentions: &discordgo.MessageAllowedMentions{},
	}
	if hasQuestions {
		data.Components = []discordgo.MessageComponent{discordgo.ActionsRow{Components: []discordgo.MessageComponent{
			discordgo.Button{Label: "👀 Try the questions", Style: discordgo.PrimaryButton, CustomID: fmt.Sprintf("tkttry:%d", id)},
		}}}
	}
	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{Type: discordgo.InteractionResponseChannelMessageWithSource, Data: data}); err != nil {
		appLogger.Warnf("Could not answer /tickettype show: %v", err)
	}
}

func typeShowEmbed(s *discordgo.Session, guildID string, id int64) (*discordgo.MessageEmbed, bool, error) {
	tt, err := needType(guildID, id)
	if err != nil {
		return nil, false, err
	}
	questions, err := tickets.Questions(id)
	if err != nil {
		return nil, false, err
	}
	staff, err := ticketStaffRoles(s, tt)
	if err != nil {
		return nil, false, err
	}
	staffNames := mentionAll(staff)
	if staffNames == "" {
		staffNames = "⚠️ none (no tickets can open)"
	}
	state := "🔴 Closed to new tickets"
	if tt.Open {
		state = "🟢 Open"
	}
	yesNo := func(b bool) string {
		if b {
			return "yes"
		}
		return "no"
	}
	lines := []string{
		fmt.Sprintf("%s · tickets go in <#%s> · logs in <#%s>", state, tt.CategoryID, tt.LogChannelID),
		"Staff: " + staffNames,
		fmt.Sprintf("Open tickets per member: %d · DM transcript on close: %s · ping staff on open: %s", tt.MaxOpen, yesNo(tt.DMTranscript), yesNo(tt.PingStaff)),
	}
	if tt.Intro != "" {
		lines = append(lines, "Intro: "+tt.Intro)
	}
	if tt.Welcome != "" {
		lines = append(lines, "Message in each new ticket:\n> "+strings.ReplaceAll(shorten(tt.Welcome, 200), "\n", "\n> "))
	}
	if tt.Header != "" {
		lines = append(lines, "Text at the top of the questions:\n> "+strings.ReplaceAll(shorten(tt.Header, 300), "\n", "\n> "))
	}
	lines = append(lines, "")
	for n, q := range questions {
		line := fmt.Sprintf("`%d` %s", n+1, q.Describe())
		if q.Help != "" {
			line += "\n   *" + q.Help + "*"
		}
		lines = append(lines, line)
	}
	if len(questions) == 0 {
		lines = append(lines, "No intake questions: pressing the button opens the ticket straight away. Add some with `/tickettype question add`.")
	} else {
		lines = append(lines, fmt.Sprintf("\n%d question(s), shown as %d pop-up(s) of up to %d before the ticket opens.",
			len(questions), len(formPages(questions, tt.Header != "")), formPerPage))
	}
	return &discordgo.MessageEmbed{
		Title:       truncateRunes(fmt.Sprintf("🎫 %s%s (#%d)", emojiPrefix(tt.Emoji), tt.Name, tt.ID), 256),
		Description: truncateRunes(strings.Join(lines, "\n"), 4000),
		Color:       ticketColor,
	}, len(questions) > 0, nil
}

func typePost(s *discordgo.Session, i *discordgo.InteractionCreate, opts cmdOpts, tt *ticketType) (string, error) {
	channelID := i.ChannelID
	if ch := optChannel(i, opts, "channel"); ch != nil {
		channelID = ch.ID
	}
	if err := checkPanelChannel(s, channelID); err != nil {
		return "", err
	}
	intro := tt.Intro
	if intro == "" {
		intro = "Press the button below to open a private ticket with staff."
	}
	_, err := s.ChannelMessageSendComplex(channelID, &discordgo.MessageSend{
		Embeds:          []*discordgo.MessageEmbed{{Title: "🎫 " + tt.Name, Description: intro, Color: ticketColor}},
		Components:      ticketPanelButtons([]ticketType{*tt}),
		AllowedMentions: &discordgo.MessageAllowedMentions{},
	})
	if err != nil {
		return "", userErrorf("I couldn't post in <#%s>. Check my permissions there.", channelID)
	}
	note := ""
	if !tt.Open {
		note = " It's closed to new tickets for now: open it with `/tickettype edit open:True`."
	}
	return fmt.Sprintf("✅ Posted **%s** in <#%s>.%s", tt.Name, channelID, note), nil
}

// ---- questions -----------------------------------------------------------------------------

func questionAdd(opts cmdOpts, tt *ticketType) (string, error) {
	existing, err := tickets.Questions(tt.ID)
	if err != nil {
		return "", err
	}
	if len(existing) >= formMaxQuestions {
		return "", userErrorf("A ticket type can have at most %d questions.", formMaxQuestions)
	}
	label, _ := optString(opts, "question")
	label = strings.TrimSpace(label)
	kind, ok := optString(opts, "kind")
	if !ok {
		kind = kindShort
	}
	required, ok := optBool(opts, "required")
	if !ok {
		required = true
	}
	help, _ := optString(opts, "help")
	help = strings.TrimSpace(help)
	choicesText, _ := optString(opts, "choices")
	choices := parseChoices(choicesText)
	maxLength, _ := optInt(opts, "max_length")
	if problems := questionProblems(label, kind, help, choices, int(maxLength)); len(problems) > 0 {
		return "", userErrorf("%s", strings.Join(problems, "\n"))
	}
	q := formQuestion{Label: label, Kind: kind, Required: required, Help: help, Choices: choices, MaxLength: int(maxLength)}
	if err := tickets.AddQuestion(tt.ID, q); err != nil {
		return "", err
	}
	count := len(existing) + 1
	pages := len(formPages(append(existing, q), tt.Header != ""))
	return fmt.Sprintf("✅ Question %d added to **%s**: %s (%s). Members now answer %d question(s) on %d pop-up(s) before their ticket opens.",
		count, tt.Name, label, kindName(kind), count, pages), nil
}

// numberedQuestion is question number (from 1) of a ticket type.
func numberedQuestion(tt *ticketType, number int64) ([]formQuestion, formQuestion, error) {
	questions, err := tickets.Questions(tt.ID)
	if err != nil {
		return nil, formQuestion{}, err
	}
	if number < 1 || int(number) > len(questions) {
		if len(questions) == 0 {
			return nil, formQuestion{}, userErrorf("**%s** has no questions yet.", tt.Name)
		}
		return nil, formQuestion{}, userErrorf("**%s** has questions 1 to %d. `/tickettype show` lists them.", tt.Name, len(questions))
	}
	return questions, questions[number-1], nil
}

func questionRemove(opts cmdOpts, tt *ticketType) (string, error) {
	number, _ := optInt(opts, "number")
	questions, q, err := numberedQuestion(tt, number)
	if err != nil {
		return "", err
	}
	if err := tickets.RemoveQuestion(tt.ID, q.ID); err != nil {
		return "", err
	}
	var order []int64
	for _, other := range questions {
		if other.ID != q.ID {
			order = append(order, other.ID)
		}
	}
	if err := tickets.ReorderQuestions(tt.ID, order); err != nil {
		return "", err
	}
	return fmt.Sprintf("✅ Removed question %d (%s) from **%s**. Tickets already open keep their answers.", number, q.Label, tt.Name), nil
}

func questionMove(opts cmdOpts, tt *ticketType) (string, error) {
	number, _ := optInt(opts, "number")
	to, _ := optInt(opts, "to")
	questions, q, err := numberedQuestion(tt, number)
	if err != nil {
		return "", err
	}
	if to < 1 || int(to) > len(questions) {
		return "", userErrorf("Pick a new number from 1 to %d.", len(questions))
	}
	if err := tickets.ReorderQuestions(tt.ID, moveID(questions, q.ID, int(to))); err != nil {
		return "", err
	}
	return fmt.Sprintf("✅ Moved \"%s\" to number %d on **%s**.", q.Label, to, tt.Name), nil
}

// moveID is the question IDs in order with id moved to position to (from 1).
func moveID(questions []formQuestion, id int64, to int) []int64 {
	var order []int64
	for _, q := range questions {
		if q.ID != id {
			order = append(order, q.ID)
		}
	}
	at := to - 1
	order = append(order, 0)
	copy(order[at+1:], order[at:])
	order[at] = id
	return order
}

// ---- delete: confirm with buttons ------------------------------------------------------------

func typeDeleteConfirm(s *discordgo.Session, i *discordgo.InteractionCreate, opts cmdOpts) {
	id, _ := optInt(opts, "type")
	tt, err := needType(i.GuildID, id)
	if err != nil {
		respondEphemeral(s, i, errorText("/tickettype delete", err))
		return
	}
	all, err := tickets.ListTickets(i.GuildID, id, "")
	if err != nil {
		respondEphemeral(s, i, errorText("/tickettype delete", err))
		return
	}
	open := 0
	for _, t := range all {
		if t.Status == ticketOpen {
			open++
		}
	}
	err = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: fmt.Sprintf("Delete **%s**, its questions and the records of its **%d** ticket(s) (%d still open)? Ticket channels and logged transcripts stay. This can't be undone. (To stop new tickets but keep everything, use `/tickettype edit open:False`.)",
				tt.Name, len(all), open),
			Flags: discordgo.MessageFlagsEphemeral,
			Components: []discordgo.MessageComponent{discordgo.ActionsRow{Components: []discordgo.MessageComponent{
				discordgo.Button{Label: "Delete", Style: discordgo.DangerButton, CustomID: fmt.Sprintf("tktdel:%d:yes", id)},
				discordgo.Button{Label: "Cancel", Style: discordgo.SecondaryButton, CustomID: fmt.Sprintf("tktdel:%d:no", id)},
			}}},
		},
	})
	if err != nil {
		appLogger.Warnf("Could not answer /tickettype delete: %v", err)
	}
}

func handleTypeDeleteButton(s *discordgo.Session, i *discordgo.InteractionCreate, customID string) {
	parts := strings.Split(customID, ":")
	id, ok := parseID(parts, 1)
	if !ok || len(parts) != 3 {
		respondEphemeral(s, i, "This button no longer does anything.")
		return
	}
	if ticketsOff(s, i) {
		return
	}
	content := "Cancelled. Nothing was deleted."
	if parts[2] == "yes" {
		switch tt, err := tickets.GetType(i.GuildID, id); {
		case !canManageServer(i):
			content = "You need Manage Server (or the staff role) to delete ticket types."
		case err != nil:
			content = errorText("Deleting a ticket type", err)
		case tt == nil:
			content = "That ticket type was already deleted."
		default:
			if err := tickets.DeleteType(id); err != nil {
				content = errorText("Deleting a ticket type", err)
			} else {
				content = fmt.Sprintf("Deleted **%s**. Its buttons now say the ticket type is gone.", tt.Name)
			}
		}
	}
	updateMessage(s, i, content, nil)
}

// ---- header and welcome: a pop-up with one big box ----------------------------------------

func typeTextBox(s *discordgo.Session, i *discordgo.InteractionCreate, which string, opts cmdOpts) {
	id, _ := optInt(opts, "type")
	tt, err := needType(i.GuildID, id)
	if err != nil {
		respondEphemeral(s, i, errorText("/tickettype "+which, err))
		return
	}
	title, label, help, current, limit := "Text above "+tt.Name+"'s questions", "What to show above the questions",
		"New lines and **bold** work. Leave it empty to remove the text.", tt.Header, formHeaderMax
	if which == "welcome" {
		title, label, help, current, limit = "Message in each "+tt.Name+" ticket", "Posted in each new ticket",
			"{member} pings them; {name} and {type} work too. Leave it empty to remove it.", tt.Welcome, formWelcomeMax
	}
	required := false
	err = showModal(s, i, &discordgo.InteractionResponseData{
		CustomID: fmt.Sprintf("tkttext:%d:%s", tt.ID, which),
		Title:    truncateRunes(title, 45),
		Components: []discordgo.MessageComponent{discordgo.Label{
			Label: label, Description: help,
			Component: discordgo.TextInput{CustomID: "text", Style: discordgo.TextInputParagraph, Required: &required, MaxLength: limit, Value: current},
		}},
	})
	if err != nil {
		appLogger.Warnf("Could not open the /tickettype %s pop-up: %v", which, err)
	}
}

func handleTypeTextSubmit(s *discordgo.Session, i *discordgo.InteractionCreate, customID string) {
	parts := strings.Split(customID, ":")
	id, ok := parseID(parts, 1)
	if !ok || len(parts) != 3 || (parts[2] != "header" && parts[2] != "welcome") {
		respondEphemeral(s, i, "That pop-up has expired. Please try again.")
		return
	}
	if ticketsOff(s, i) {
		return
	}
	if !canManageServer(i) {
		respondEphemeral(s, i, "You need Manage Server (or the staff role) to change ticket types.")
		return
	}
	msg, err := saveTypeText(i.GuildID, id, parts[2], strings.TrimSpace(modalValues(i)["text"].text()))
	if err != nil {
		msg = errorText("Saving a ticket type's text", err)
	}
	respondEphemeral(s, i, msg)
}

func saveTypeText(guildID string, id int64, which, text string) (string, error) {
	tt, err := needType(guildID, id)
	if err != nil {
		return "", err
	}
	if which == "header" {
		tt.Header = truncateRunes(text, formHeaderMax)
	} else {
		tt.Welcome = truncateRunes(text, formWelcomeMax)
	}
	if err := tickets.UpdateType(tt); err != nil {
		return "", err
	}
	if which == "header" {
		if text == "" {
			return fmt.Sprintf("✅ **%s** has no text above its questions now.", tt.Name), nil
		}
		return fmt.Sprintf("✅ Saved. It shows above **%s**'s first questions (it only shows when the type has questions), leaving room for %d questions on that pop-up.",
			tt.Name, formPerPage-1), nil
	}
	if text == "" {
		return fmt.Sprintf("✅ New **%s** tickets get only the usual message now.", tt.Name), nil
	}
	var words []string
	for _, w := range formWelcomeWords {
		words = append(words, fmt.Sprintf("`%s` %s", w.Word, w.What))
	}
	return fmt.Sprintf("✅ Saved. Each new **%s** ticket gets your message right after the opening message. In it, %s.",
		tt.Name, strings.Join(words, ", ")), nil
}
