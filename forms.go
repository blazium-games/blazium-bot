package main

import (
	"fmt"
	"strings"

	"github.com/bwmarrin/discordgo"
)

// Forms: members press a button, answer questions in pop-ups, and their submission is
// posted in the form's channel. For game jam entries, event sign-ups, feedback… With
// review on, each submission gets Accept, Decline and staff votes (applications).
//
//	/form create|edit|show|post|header|list|delete     set up forms (Manage Server)
//	/form question add|remove|move                    the questions
//	/form submissions|view                            what members sent
//
// A form's button can go on a panel with ticket types (/ticket panel).

const formColor = 0x5865F2

var subStatusWords = map[string]string{
	subReceived: "📨 received",
	subPending:  "⏳ waiting for a decision",
	subAccepted: "✅ accepted",
	subDeclined: "❌ declined",
}

func initForms() {
	db := botDatabase()
	if db == nil {
		appLogger.Warn("Postgres is unavailable; forms disabled")
		return
	}
	store, err := newPostgresFormsStore(db)
	if err != nil {
		appLogger.Errorf("Forms disabled: %v", err)
		return
	}
	appForms = store
	appLogger.Info("Forms ready")
}

func formOption(required bool) *discordgo.ApplicationCommandOption {
	return &discordgo.ApplicationCommandOption{
		Type: discordgo.ApplicationCommandOptionInteger, Name: "form", Description: "Which form",
		Required: required, Autocomplete: true, MinValue: floatPtr(1),
	}
}

func formCommandOptions() []*discordgo.ApplicationCommandOption {
	kinds := make([]*discordgo.ApplicationCommandOptionChoice, len(formKinds))
	for n, k := range formKinds {
		kinds[n] = &discordgo.ApplicationCommandOptionChoice{Name: k.Name, Value: k.Key}
	}
	kind := strOpt("kind", "Short answer, long answer, pick one or pick any (default: short)", false, 0)
	kind.Choices = kinds
	status := strOpt("status", "Only these (all if left empty)", false, 0)
	status.Choices = []*discordgo.ApplicationCommandOptionChoice{
		{Name: "Received (forms without review)", Value: subReceived},
		{Name: "Waiting for a decision", Value: subPending},
		{Name: "Accepted", Value: subAccepted},
		{Name: "Declined", Value: subDeclined},
	}
	return []*discordgo.ApplicationCommandOption{
		{
			Type: discordgo.ApplicationCommandOptionSubCommandGroup, Name: "question", Description: "The questions on a form",
			Options: []*discordgo.ApplicationCommandOption{
				sub("add", "Add a question to the end of a form",
					formOption(true),
					strOpt("question", fmt.Sprintf("The question (up to %d characters)", formLabelMax), true, 100),
					kind,
					boolOpt("required", "Whether it must be answered (yes if empty)"),
					strOpt("help", fmt.Sprintf("A line of help under the question (up to %d characters)", formHelpMax), false, 200),
					strOpt("choices", "For pick one / pick any: the choices, separated by commas", false, 3000),
					intOpt("max_length", "For typed answers: the most characters allowed", false, 1, 1000),
				),
				sub("remove", "Remove a question from a form", formOption(true),
					intOpt("number", "The question's number, from /form show", true, 1, formMaxQuestions)),
				sub("move", "Move a question to another place in a form", formOption(true),
					intOpt("number", "The question's number now", true, 1, formMaxQuestions),
					intOpt("to", "Its new number", true, 1, formMaxQuestions)),
			},
		},
		sub("create", "Make a new form (game jam entries, sign-ups, applications…)",
			strOpt("name", fmt.Sprintf("The form's name, shown at the top of the pop-up (up to %d characters)", formNameMax), true, formNameMax),
			channelOpt("channel", "A staff-only channel where submissions are posted", true, panelTextChannels...),
			strOpt("intro", "What the form is for, shown above its button", false, formIntroMax),
			boolOpt("review", "Accept or decline each submission, with staff votes (default: no)"),
			intOpt("max_per_member", "Submissions one member may send, not counting declined ones (0: no limit; default 1)", false, 0, 100),
			strOpt("button_label", "Words on the form's button (default: 📝 Fill in)", false, 80),
			strOpt("emoji", "The emoji on its button in a panel", false, 60),
			boolOpt("allow_public", "Allow a channel everyone can see (submissions would be public)"),
		),
		sub("edit", "Change a form. Only the options you fill in change.",
			formOption(true),
			strOpt("name", "A new name", false, formNameMax),
			strOpt("intro", "New words above its button", false, formIntroMax),
			channelOpt("channel", "A new channel for submissions", false, panelTextChannels...),
			boolOpt("open", "Whether the form takes submissions"),
			intOpt("max_per_member", "Submissions one member may send, not counting declined ones (0: no limit)", false, 0, 100),
			boolOpt("dm_receipt", "Send members a copy of what they sent by DM"),
			boolOpt("review", "Accept or decline each submission, with staff votes"),
			roleOpt("accept_role", "With review: a role to give when a submission is accepted", false),
			boolOpt("remove_accept_role", "Stop giving a role on acceptance"),
			roleOpt("reviewer_role", "With review: a role that may accept and decline, besides Manage Server", false),
			boolOpt("dm_accept", "With review: tell members by DM when accepted"),
			boolOpt("dm_decline", "With review: tell members by DM when declined"),
			strOpt("button_label", "Words on the form's button (type none for the default)", false, 80),
			strOpt("emoji", "The emoji on its button in a panel (type none to remove it)", false, 60),
			boolOpt("allow_public", "Allow a channel everyone can see"),
		),
		sub("show", "A form's settings and questions, with a button to try it", formOption(true)),
		sub("post", "Post a form with its button", formOption(true),
			channelOpt("channel", "Where to post it (this channel if left empty)", false, panelTextChannels...)),
		sub("header", "Write a block of text shown at the top of a form, above its questions", formOption(true)),
		sub("list", "Every form in this server"),
		sub("delete", "Delete a form, its questions and every submission sent to it", formOption(true)),
		sub("submissions", "Submissions members have sent, newest first", formOption(false), status),
		sub("view", "One submission in full, with Accept and Decline if it's waiting",
			intOpt("number", "The submission's number", true, 1, 1e15)),
	}
}

func registerForms() {
	if _, exists := CommandManager.GetCommand("form"); !exists {
		err := CommandManager.RegisterCommand("form", &SlashCommandHandler{
			Name: "form", Description: "Forms members fill in: game jam entries, sign-ups, applications…",
			Handler: handleFormCommand, Options: formCommandOptions(), Permissions: []string{"user"},
			GuildOnly: true, DefaultPermissions: &permManageGuild,
		})
		if err != nil {
			appLogger.Errorf("Failed to register form command handler: %v", err)
		}
	}
	registerAutocompleteRoute("form", handleFormAutocomplete)
	registerComponentRoute("form", handleFormButton)
	registerComponentRoute("formtry", handleFormTryButton)
	registerComponentRoute("formnext", handleFormNextButton)
	registerComponentRoute("formdel", handleFormDeleteButton)
	registerComponentRoute("formsub", handleSubmissionButton)
	registerModalRoute("formpage", handleFormPageSubmit)
	registerModalRoute("formtext", handleFormTextSubmit)
	registerModalRoute("formnote", handleFormNoteSubmit)
}

func handleFormAutocomplete(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if appForms == nil || i.GuildID == "" || !canManageServer(i) {
		respondChoices(s, i, nil)
		return
	}
	typed := ""
	if f := focusedOption(i); f != nil {
		if f.Name != "form" {
			respondChoices(s, i, nil)
			return
		}
		typed = strings.ToLower(fmt.Sprint(f.Value))
	}
	forms, err := appForms.ListForms(i.GuildID)
	if err != nil {
		respondChoices(s, i, nil)
		return
	}
	choices := []*discordgo.ApplicationCommandOptionChoice{}
	for _, f := range forms {
		name := fmt.Sprintf("%s (#%d)", f.Name, f.ID)
		if typed == "" || strings.Contains(strings.ToLower(name), typed) {
			choices = append(choices, &discordgo.ApplicationCommandOptionChoice{Name: truncateRunes(name, 100), Value: f.ID})
		}
	}
	respondChoices(s, i, choices)
}

func needForm(guildID string, id int64) (*appForm, error) {
	f, err := appForms.GetForm(guildID, id)
	if err != nil {
		return nil, err
	}
	if f == nil {
		return nil, userErrorf("I can't find that form. `/form list` shows the forms in this server.")
	}
	return f, nil
}

func formsOff(s *discordgo.Session, i *discordgo.InteractionCreate) bool {
	if appForms == nil {
		respondEphemeral(s, i, "Forms need the bot's database, which isn't available right now.")
		return true
	}
	if i.GuildID == "" || i.Member == nil {
		respondEphemeral(s, i, "Forms only work inside a server.")
		return true
	}
	return false
}

// checkAcceptRole: a role the bot can hand out (same rules as sign-up panels).
func checkAcceptRole(s *discordgo.Session, guildID string, role *discordgo.Role) error {
	facts, err := loadRoleFacts(s, guildID)
	if err != nil {
		return err
	}
	full, ok := facts.Roles[role.ID]
	if !ok {
		return userErrorf("I can't find that role.")
	}
	if why := roleProblem(full, facts); why != "" {
		return userErrorf("I can't give %s to accepted members: %s.", roleMention(role.ID), why)
	}
	return nil
}

// checkSubmissionChannel: the bot must be able to post there; @everyone must not see it unless allowPublic.
func checkSubmissionChannel(s *discordgo.Session, guildID, channelID string, allowPublic bool) error {
	if err := checkPanelChannel(s, channelID); err != nil {
		return err
	}
	if !allowPublic && everyoneCanSee(s, guildID, channelID) {
		return userErrorf("Everyone can see <#%s>, so every submission would be public. Pick a staff-only channel, or add `allow_public:True` if that's what you want (a public showcase, say).", channelID)
	}
	return nil
}

// ============================================================================
// /form
// ============================================================================

func handleFormCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if formsOff(s, i) {
		return
	}
	if !canManageServer(i) {
		respondEphemeral(s, i, "You need Manage Server (or the staff role) to manage forms.")
		return
	}
	name, opts := commandOptions(i)
	switch name { // these answer with a pop-up or an interactive message
	case "header":
		formHeaderBox(s, i, opts)
		return
	case "delete":
		formDeleteConfirm(s, i, opts)
		return
	case "show":
		formShow(s, i, opts)
		return
	case "view":
		submissionView(s, i, opts)
		return
	}
	if err := deferEphemeral(s, i); err != nil {
		appLogger.Warnf("Could not defer /form: %v", err)
		return
	}
	msg, err := runFormSubcommand(s, i, name, opts)
	if err != nil {
		msg = errorText("/form "+name, err)
	}
	editResponse(s, i, msg)
}

func runFormSubcommand(s *discordgo.Session, i *discordgo.InteractionCreate, name string, opts cmdOpts) (string, error) {
	switch name {
	case "create":
		return formCreate(s, i, opts)
	case "list":
		return formList(i)
	case "submissions":
		return submissionList(i, opts)
	}
	id, _ := optInt(opts, "form")
	f, err := needForm(i.GuildID, id)
	if err != nil {
		return "", err
	}
	q := questionTarget{name: f.Name, header: f.Header != "", store: formQuestionStore{f.ID}, showCmd: "/form show"}
	switch name {
	case "edit":
		return formEdit(s, i, opts, f)
	case "post":
		return formPost(s, i, opts, f)
	case "question add":
		return q.add(opts)
	case "question remove":
		return q.remove(opts)
	case "question move":
		return q.move(opts)
	}
	return "", userErrorf("Unknown subcommand.")
}

func formCreate(s *discordgo.Session, i *discordgo.InteractionCreate, opts cmdOpts) (string, error) {
	name, _ := optString(opts, "name")
	name = strings.TrimSpace(name)
	if name == "" {
		return "", userErrorf("The form needs a name.")
	}
	channel := optChannel(i, opts, "channel")
	allowPublic, _ := optBool(opts, "allow_public")
	if err := checkSubmissionChannel(s, i.GuildID, channel.ID, allowPublic); err != nil {
		return "", err
	}
	intro, _ := optString(opts, "intro")
	review, _ := optBool(opts, "review")
	maxPer, ok := optInt(opts, "max_per_member")
	if !ok {
		maxPer = 1
	}
	label, _ := optString(opts, "button_label")
	emojiText, _ := optString(opts, "emoji")
	emoji, err := buttonEmojiText(emojiText)
	if err != nil {
		return "", err
	}
	f := &appForm{
		GuildID: i.GuildID, Name: name, Intro: strings.TrimSpace(intro), ChannelID: channel.ID, ButtonLabel: strings.TrimSpace(label),
		Emoji: emoji, Open: true, MaxPerMember: int(maxPer), DMReceipt: true, Review: review, DMAccept: true, DMDecline: true,
		CreatedBy: i.Member.User.ID,
	}
	id, err := appForms.CreateForm(f)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("✅ Made the form **%s** (#%d). Submissions go to <#%s>%s.\nNext:\n1. Add questions with `/form question add form:%s`.\n2. Try it with `/form show` and **👀 Try it**.\n3. Post it with `/form post`, or with ticket types on one message with `/ticket panel`.",
		name, id, channel.ID, map[bool]string{true: " with Accept and Decline", false: ""}[review], name), nil
}

func formEdit(s *discordgo.Session, i *discordgo.InteractionCreate, opts cmdOpts, f *appForm) (string, error) {
	var changed []string
	if v, ok := optString(opts, "name"); ok && strings.TrimSpace(v) != "" {
		f.Name, changed = strings.TrimSpace(v), append(changed, "name")
	}
	if v, ok := optString(opts, "intro"); ok && strings.TrimSpace(v) != "" {
		f.Intro, changed = strings.TrimSpace(v), append(changed, "intro")
	}
	if ch := optChannel(i, opts, "channel"); ch != nil {
		allowPublic, _ := optBool(opts, "allow_public")
		if err := checkSubmissionChannel(s, i.GuildID, ch.ID, allowPublic); err != nil {
			return "", err
		}
		f.ChannelID, changed = ch.ID, append(changed, "channel")
	}
	for _, b := range []struct {
		opt   string
		field *bool
	}{{"open", &f.Open}, {"dm_receipt", &f.DMReceipt}, {"review", &f.Review}, {"dm_accept", &f.DMAccept}, {"dm_decline", &f.DMDecline}} {
		if v, ok := optBool(opts, b.opt); ok {
			*b.field, changed = v, append(changed, strings.ReplaceAll(b.opt, "_", " "))
		}
	}
	if v, ok := optInt(opts, "max_per_member"); ok {
		f.MaxPerMember, changed = int(v), append(changed, "max per member")
	}
	reviewer := optRole(i, opts, "reviewer_role")
	if accept := optRole(i, opts, "accept_role"); accept != nil {
		if err := checkAcceptRole(s, i.GuildID, accept); err != nil {
			return "", err
		}
		if accept.ID == f.ReviewerRoleID || (reviewer != nil && reviewer.ID == accept.ID) {
			return "", userErrorf("The reviewer role can't be the role accepted members get.")
		}
		f.AcceptRoleID, changed = accept.ID, append(changed, "accept role")
	} else if remove, _ := optBool(opts, "remove_accept_role"); remove {
		f.AcceptRoleID, changed = "", append(changed, "accept role")
	}
	if reviewer != nil {
		if reviewer.ID == f.AcceptRoleID || reviewer.ID == i.GuildID {
			return "", userErrorf("Pick a staff role as the reviewer role.")
		}
		f.ReviewerRoleID, changed = reviewer.ID, append(changed, "reviewer role")
	}
	if v, ok := optString(opts, "button_label"); ok {
		v = strings.TrimSpace(v)
		if strings.EqualFold(v, "none") {
			v = ""
		}
		f.ButtonLabel, changed = v, append(changed, "button label")
	}
	if text, ok := optString(opts, "emoji"); ok {
		emoji, err := buttonEmojiText(text)
		if err != nil {
			return "", err
		}
		f.Emoji, changed = emoji, append(changed, "emoji")
	}
	if len(changed) == 0 {
		return "", userErrorf("Fill in at least one thing to change.")
	}
	if err := appForms.UpdateForm(f); err != nil {
		return "", err
	}
	note := ""
	if !f.Review && (f.AcceptRoleID != "" || f.ReviewerRoleID != "") {
		note = " (Review is off, so the accept and reviewer roles aren't used until you turn it on.)"
	}
	return fmt.Sprintf("✅ Changed **%s**: %s. Posted buttons keep working.%s", f.Name, strings.Join(changed, ", "), note), nil
}

func formList(i *discordgo.InteractionCreate) (string, error) {
	forms, err := appForms.ListForms(i.GuildID)
	if err != nil {
		return "", err
	}
	if len(forms) == 0 {
		return "No forms yet. Make one with `/form create`.", nil
	}
	var lines []string
	for _, f := range forms {
		subs, err := appForms.ListSubmissions(i.GuildID, f.ID, "")
		if err != nil {
			return "", err
		}
		waiting := 0
		for _, sub := range subs {
			if sub.Status == subPending {
				waiting++
			}
		}
		state := "🔴 closed"
		if f.Open {
			state = "🟢 open"
		}
		line := fmt.Sprintf("%s**%s** (#%d) · %s · %d submission(s) · to <#%s>", emojiPrefix(f.Emoji), f.Name, f.ID, state, len(subs), f.ChannelID)
		if f.Review {
			line += fmt.Sprintf(" · %d waiting for review", waiting)
		}
		lines = append(lines, line)
	}
	return fitLines(lines[0], lines[1:], func(left int) string {
		return fmt.Sprintf("…and %d more. `/form show` opens any form by name.", left)
	}), nil
}

func formShow(s *discordgo.Session, i *discordgo.InteractionCreate, opts cmdOpts) {
	id, _ := optInt(opts, "form")
	embed, hasQuestions, err := formShowEmbed(i.GuildID, id)
	if err != nil {
		respondEphemeral(s, i, errorText("/form show", err))
		return
	}
	data := &discordgo.InteractionResponseData{Embeds: []*discordgo.MessageEmbed{embed}, Flags: discordgo.MessageFlagsEphemeral, AllowedMentions: &discordgo.MessageAllowedMentions{}}
	if hasQuestions {
		data.Components = []discordgo.MessageComponent{discordgo.ActionsRow{Components: []discordgo.MessageComponent{
			discordgo.Button{Label: "👀 Try it", Style: discordgo.PrimaryButton, CustomID: fmt.Sprintf("formtry:%d", id)},
		}}}
	}
	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{Type: discordgo.InteractionResponseChannelMessageWithSource, Data: data}); err != nil {
		appLogger.Warnf("Could not answer /form show: %v", err)
	}
}

func formShowEmbed(guildID string, id int64) (*discordgo.MessageEmbed, bool, error) {
	f, err := needForm(guildID, id)
	if err != nil {
		return nil, false, err
	}
	questions, err := appForms.Questions(id)
	if err != nil {
		return nil, false, err
	}
	state := "🔴 Closed"
	if f.Open {
		state = "🟢 Taking submissions"
	}
	limit := "no limit"
	if f.MaxPerMember > 0 {
		limit = fmt.Sprint(f.MaxPerMember)
	}
	yesNo := map[bool]string{true: "yes", false: "no"}
	lines := []string{
		fmt.Sprintf("%s · submissions go to <#%s>", state, f.ChannelID),
		fmt.Sprintf("Button: %s%s · per member: %s · DM receipt: %s", emojiPrefix(f.Emoji), formButtonLabel(f), limit, yesNo[f.DMReceipt]),
	}
	if f.Review {
		role, reviewers := "no role", ""
		if f.AcceptRoleID != "" {
			role = roleMention(f.AcceptRoleID)
		}
		if f.ReviewerRoleID != "" {
			reviewers = " and " + roleMention(f.ReviewerRoleID)
		}
		lines = append(lines, fmt.Sprintf("Review: on · reviewers: Manage Server%s · accepted members get %s · DM when accepted: %s, declined: %s",
			reviewers, role, yesNo[f.DMAccept], yesNo[f.DMDecline]))
	} else {
		lines = append(lines, "Review: off (submissions are just received; turn on with `/form edit review:True`)")
	}
	if f.Intro != "" {
		lines = append(lines, "Intro: "+f.Intro)
	}
	if f.Header != "" {
		lines = append(lines, "Text at the top of the form:\n> "+strings.ReplaceAll(shorten(f.Header, 300), "\n", "\n> "))
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
		lines = append(lines, "No questions yet: `/form question add`.")
	} else {
		lines = append(lines, fmt.Sprintf("\n%d question(s), shown as %d pop-up(s) of up to %d.", len(questions), len(formPages(questions, f.Header != "")), formPerPage))
	}
	return &discordgo.MessageEmbed{
		Title: truncateRunes(fmt.Sprintf("📝 %s (#%d)", f.Name, f.ID), 256), Description: truncateRunes(strings.Join(lines, "\n"), 4000), Color: formColor,
	}, len(questions) > 0, nil
}

func formPost(s *discordgo.Session, i *discordgo.InteractionCreate, opts cmdOpts, f *appForm) (string, error) {
	questions, err := appForms.Questions(f.ID)
	if err != nil {
		return "", err
	}
	if len(questions) == 0 {
		return "", userErrorf("**%s** has no questions. Add some with `/form question add` first.", f.Name)
	}
	channelID := i.ChannelID
	if ch := optChannel(i, opts, "channel"); ch != nil {
		channelID = ch.ID
	}
	if err := checkPanelChannel(s, channelID); err != nil {
		return "", err
	}
	intro := f.Intro
	if intro == "" {
		intro = "Press the button below to fill in the form."
	}
	_, err = s.ChannelMessageSendComplex(channelID, &discordgo.MessageSend{
		Embeds: []*discordgo.MessageEmbed{{Title: "📝 " + f.Name, Description: intro, Color: formColor}},
		Components: []discordgo.MessageComponent{discordgo.ActionsRow{Components: []discordgo.MessageComponent{
			formFillButton(*f, false), formMineButton(f.ID),
		}}},
		AllowedMentions: &discordgo.MessageAllowedMentions{},
	})
	if err != nil {
		return "", userErrorf("I couldn't post in <#%s>. Check my permissions there.", channelID)
	}
	note := ""
	if !f.Open {
		note = " It's closed for now: open it with `/form edit open:True`."
	}
	return fmt.Sprintf("✅ Posted **%s** in <#%s>.%s", f.Name, channelID, note), nil
}

// ---- delete: confirm with buttons ------------------------------------------------------------

func formDeleteConfirm(s *discordgo.Session, i *discordgo.InteractionCreate, opts cmdOpts) {
	id, _ := optInt(opts, "form")
	f, err := needForm(i.GuildID, id)
	if err != nil {
		respondEphemeral(s, i, errorText("/form delete", err))
		return
	}
	subs, err := appForms.ListSubmissions(i.GuildID, id, "")
	if err != nil {
		respondEphemeral(s, i, errorText("/form delete", err))
		return
	}
	err = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: fmt.Sprintf("Delete **%s**, its questions and the **%d** submission(s) sent to it? Messages already posted stay. This can't be undone. (To stop new submissions but keep them, use `/form edit open:False`.)", f.Name, len(subs)),
			Flags:   discordgo.MessageFlagsEphemeral,
			Components: []discordgo.MessageComponent{discordgo.ActionsRow{Components: []discordgo.MessageComponent{
				discordgo.Button{Label: "Delete", Style: discordgo.DangerButton, CustomID: fmt.Sprintf("formdel:%d:yes", id)},
				discordgo.Button{Label: "Cancel", Style: discordgo.SecondaryButton, CustomID: fmt.Sprintf("formdel:%d:no", id)},
			}}},
		},
	})
	if err != nil {
		appLogger.Warnf("Could not answer /form delete: %v", err)
	}
}

func handleFormDeleteButton(s *discordgo.Session, i *discordgo.InteractionCreate, customID string) {
	parts := strings.Split(customID, ":")
	id, ok := parseID(parts, 1)
	if !ok || len(parts) != 3 {
		respondEphemeral(s, i, "This button no longer does anything.")
		return
	}
	if formsOff(s, i) {
		return
	}
	content := "Cancelled. Nothing was deleted."
	if parts[2] == "yes" {
		switch f, err := appForms.GetForm(i.GuildID, id); {
		case !canManageServer(i):
			content = "You need Manage Server (or the staff role) to delete forms."
		case err != nil:
			content = errorText("Deleting a form", err)
		case f == nil:
			content = "That form was already deleted."
		default:
			if err := appForms.DeleteForm(id); err != nil {
				content = errorText("Deleting a form", err)
			} else {
				content = fmt.Sprintf("Deleted **%s**. Its buttons now say the form is gone.", f.Name)
			}
		}
	}
	updateMessage(s, i, content, nil)
}

// ---- header: a pop-up with one big box ---------------------------------------------------------

func formHeaderBox(s *discordgo.Session, i *discordgo.InteractionCreate, opts cmdOpts) {
	id, _ := optInt(opts, "form")
	f, err := needForm(i.GuildID, id)
	if err != nil {
		respondEphemeral(s, i, errorText("/form header", err))
		return
	}
	required := false
	err = showModal(s, i, &discordgo.InteractionResponseData{
		CustomID: fmt.Sprintf("formtext:%d:header", f.ID),
		Title:    truncateRunes("Text at the top of "+f.Name, 45),
		Components: []discordgo.MessageComponent{discordgo.Label{
			Label: "What to show above the questions", Description: "New lines and **bold** work. Leave it empty to remove the text.",
			Component: discordgo.TextInput{CustomID: "text", Style: discordgo.TextInputParagraph, Required: &required, MaxLength: formHeaderMax, Value: f.Header},
		}},
	})
	if err != nil {
		appLogger.Warnf("Could not open the /form header pop-up: %v", err)
	}
}

func handleFormTextSubmit(s *discordgo.Session, i *discordgo.InteractionCreate, customID string) {
	parts := strings.Split(customID, ":")
	id, ok := parseID(parts, 1)
	if !ok || len(parts) != 3 || parts[2] != "header" {
		respondEphemeral(s, i, "That pop-up has expired. Please try again.")
		return
	}
	if formsOff(s, i) {
		return
	}
	if !canManageServer(i) {
		respondEphemeral(s, i, "You need Manage Server (or the staff role) to change forms.")
		return
	}
	f, err := needForm(i.GuildID, id)
	if err == nil {
		f.Header = truncateRunes(strings.TrimSpace(modalValues(i)["text"].text()), formHeaderMax)
		err = appForms.UpdateForm(f)
	}
	switch {
	case err != nil:
		respondEphemeral(s, i, errorText("Saving a form's header", err))
	case f.Header == "":
		respondEphemeral(s, i, fmt.Sprintf("✅ **%s** has no text at the top now.", f.Name))
	default:
		respondEphemeral(s, i, fmt.Sprintf("✅ Saved. It shows above **%s**'s first questions, leaving room for %d questions on that pop-up.", f.Name, formPerPage-1))
	}
}
