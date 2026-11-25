package main

import (
	"fmt"
	"strings"

	"github.com/bwmarrin/discordgo"
)

// Application forms: staff build a form, post it with an Apply button, and review what members send.
//
//	/form create|edit|delete|list|show|post|header|welcome|chat|panel   build and post forms (Manage Server)
//	/form question add|remove|move                                     the questions on a form
//	/applications list|view                                            applications sent, and one in full
//
// Ported from FlareBot's applications cog. Buttons and pop-ups keep their state in their
// custom IDs (see forms_apply.go and forms_review.go), except a half-filled multi-page
// form, which is kept in memory for 15 minutes.

const formColor = 0x5865F2

var (
	colorAccepted = 0x57F287
	colorDeclined = 0xED4245
)

var entryStatusWords = map[string]string{
	entryPending:  "⏳ waiting for a decision",
	entryAccepted: "✅ accepted",
	entryDeclined: "❌ declined",
}

func initForms() {
	db := botDatabase()
	if db == nil {
		appLogger.Warn("Postgres is unavailable; application forms disabled")
		return
	}
	store, err := newPostgresFormsStore(db)
	if err != nil {
		appLogger.Errorf("Application forms disabled: %v", err)
		return
	}
	appForms = store
	appLogger.Info("Application forms ready")
}

// ============================================================================
// COMMAND DEFINITIONS
// ============================================================================

func formOption(required bool) *discordgo.ApplicationCommandOption {
	return &discordgo.ApplicationCommandOption{
		Type: discordgo.ApplicationCommandOptionInteger, Name: "form", Description: "Which form",
		Required: required, Autocomplete: true, MinValue: floatPtr(1),
	}
}

func sub(name, description string, options ...*discordgo.ApplicationCommandOption) *discordgo.ApplicationCommandOption {
	return &discordgo.ApplicationCommandOption{Type: discordgo.ApplicationCommandOptionSubCommand, Name: name, Description: description, Options: options}
}

func strOpt(name, description string, required bool, maxLength int) *discordgo.ApplicationCommandOption {
	return &discordgo.ApplicationCommandOption{Type: discordgo.ApplicationCommandOptionString, Name: name, Description: description, Required: required, MaxLength: maxLength}
}

func boolOpt(name, description string) *discordgo.ApplicationCommandOption {
	return &discordgo.ApplicationCommandOption{Type: discordgo.ApplicationCommandOptionBoolean, Name: name, Description: description}
}

func roleOpt(name, description string, required bool) *discordgo.ApplicationCommandOption {
	return &discordgo.ApplicationCommandOption{Type: discordgo.ApplicationCommandOptionRole, Name: name, Description: description, Required: required}
}

func channelOpt(name, description string, required bool, types ...discordgo.ChannelType) *discordgo.ApplicationCommandOption {
	return &discordgo.ApplicationCommandOption{Type: discordgo.ApplicationCommandOptionChannel, Name: name, Description: description, Required: required, ChannelTypes: types}
}

func intOpt(name, description string, required bool, lo, hi float64) *discordgo.ApplicationCommandOption {
	return &discordgo.ApplicationCommandOption{Type: discordgo.ApplicationCommandOptionInteger, Name: name, Description: description, Required: required, MinValue: &lo, MaxValue: hi}
}

func formCommandOptions() []*discordgo.ApplicationCommandOption {
	kinds := make([]*discordgo.ApplicationCommandOptionChoice, len(formKinds))
	for n, k := range formKinds {
		kinds[n] = &discordgo.ApplicationCommandOptionChoice{Name: k.Name, Value: k.Key}
	}
	kind := strOpt("kind", "Short answer, long answer, pick one or pick any (default: short)", false, 0)
	kind.Choices = kinds
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
		sub("create", "Make a new application form",
			strOpt("name", fmt.Sprintf("The form's name, shown at the top of the pop-up (up to %d characters)", formNameMax), true, formNameMax),
			channelOpt("review_channel", "A staff-only channel where applications go", true, panelTextChannels...),
			strOpt("intro", "What the form is for, shown above the Apply button", false, formIntroMax),
			roleOpt("accept_role", "A role to give when an application is accepted", false),
			roleOpt("reviewer_role", "A role that may accept and decline, besides Manage Server", false),
			boolOpt("allow_public", "Allow a review channel everyone can see (not recommended)"),
		),
		sub("edit", "Change a form. Only the options you fill in change.",
			formOption(true),
			strOpt("name", "A new name", false, formNameMax),
			strOpt("intro", "New words above the Apply button", false, formIntroMax),
			channelOpt("review_channel", "A new staff-only review channel", false, panelTextChannels...),
			roleOpt("accept_role", "A new role for accepted applicants", false),
			roleOpt("reviewer_role", "A new reviewer role", false),
			boolOpt("open", "Whether the form takes applications"),
			boolOpt("remove_accept_role", "Stop giving a role on acceptance"),
			boolOpt("allow_public", "Allow a review channel everyone can see"),
			boolOpt("dm_accept", "Whether accepted applicants are told by DM"),
			boolOpt("dm_decline", "Whether declined applicants are told by DM"),
			strOpt("emoji", "The emoji on the form's button in a panel (type none to remove it)", false, 60),
		),
		sub("delete", "Delete a form, its questions and every application sent to it", formOption(true)),
		sub("list", "Every form in this server"),
		sub("show", "A form's settings and questions, with a button to try it", formOption(true)),
		sub("post", "Post a form with its Apply button", formOption(true),
			channelOpt("channel", "Where to post it (this channel if left empty)", false, panelTextChannels...)),
		sub("header", "Write a block of text shown at the top of a form, above its questions", formOption(true)),
		sub("welcome", "Write your own message, posted in each applicant's chat when it opens", formOption(true)),
		sub("panel", "Post several forms as buttons on one message (tickets, applications…)",
			strOpt("title", "The panel's heading", true, 256),
			strOpt("text", "Words under the heading (optional)", false, 2000),
			channelOpt("channel", "Where to post it (this channel if left empty)", false, panelTextChannels...)),
		sub("chat", "Open a private chat with each applicant, with staff roles you choose",
			formOption(true),
			channelOpt("category", "The category the chats go in (chats are on once this is set)", false, discordgo.ChannelTypeGuildCategory),
			roleOpt("staff_role", "A staff role that sees every chat (run again to add more)", false),
			roleOpt("remove_staff_role", "Stop a role seeing new chats", false),
			boolOpt("off", "Stop opening chats (chats already open stay)"),
		),
	}
}

func applicationsCommandOptions() []*discordgo.ApplicationCommandOption {
	status := strOpt("status", "Only these (waiting if left empty)", false, 0)
	status.Choices = []*discordgo.ApplicationCommandOptionChoice{
		{Name: "Waiting for a decision", Value: entryPending},
		{Name: "Accepted", Value: entryAccepted},
		{Name: "Declined", Value: entryDeclined},
	}
	return []*discordgo.ApplicationCommandOption{
		sub("list", "Applications sent, waiting ones first", formOption(false), status),
		sub("view", "One application in full, with Accept and Decline if it's waiting",
			intOpt("number", "The application's number", true, 1, 1e15)),
	}
}

// registerForms adds /form and /applications, their autocomplete, buttons and pop-ups.
func registerForms() {
	for _, c := range []*SlashCommandHandler{
		{Name: "form", Description: "Build application forms and post them", Handler: handleFormCommand, Options: formCommandOptions()},
		{Name: "applications", Description: "Applications members have sent", Handler: handleApplicationsCommand, Options: applicationsCommandOptions()},
	} {
		if _, exists := CommandManager.GetCommand(c.Name); exists {
			continue
		}
		c.Permissions = []string{"user"}
		c.GuildOnly = true
		c.DefaultPermissions = &permManageGuild
		if err := CommandManager.RegisterCommand(c.Name, c); err != nil {
			appLogger.Errorf("Failed to register %s command handler: %v", c.Name, err)
		}
	}
	registerAutocompleteRoute("form", handleFormAutocomplete)
	registerAutocompleteRoute("applications", handleFormAutocomplete)
	registerFormRoutes()
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
		appLogger.Warnf("Form autocomplete: %v", err)
		respondChoices(s, i, nil)
		return
	}
	choices := []*discordgo.ApplicationCommandOptionChoice{}
	for _, f := range forms {
		name := fmt.Sprintf("%s (#%d)", f.Name, f.ID)
		if typed != "" && !strings.Contains(strings.ToLower(name), typed) {
			continue
		}
		choices = append(choices, &discordgo.ApplicationCommandOptionChoice{Name: truncateRunes(name, 100), Value: f.ID})
	}
	respondChoices(s, i, choices)
}

// ============================================================================
// SHARED CHECKS
// ============================================================================

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

// checkReviewChannel: the bot must be able to post there, and @everyone must not see it unless allowPublic.
func checkReviewChannel(s *discordgo.Session, guildID, channelID string, allowPublic bool) error {
	if err := checkPanelChannel(s, channelID); err != nil {
		return err
	}
	if allowPublic {
		return nil
	}
	if everyoneCanSee(s, guildID, channelID) {
		return userErrorf("Everyone can see <#%s>, and applications hold members' private answers. Pick a staff-only channel, or add `allow_public:True` if you're sure.", channelID)
	}
	return nil
}

// everyoneCanSee reports whether @everyone has View Channel in a channel.
func everyoneCanSee(s *discordgo.Session, guildID, channelID string) bool {
	guild, err := lookupGuild(s, guildID)
	if err != nil {
		return false
	}
	ch, err := s.State.Channel(channelID)
	if err != nil {
		if ch, err = s.Channel(channelID); err != nil {
			return false
		}
	}
	var perms int64
	for _, r := range guild.Roles {
		if r.ID == guildID {
			perms = r.Permissions
		}
	}
	if perms&discordgo.PermissionAdministrator != 0 {
		return true
	}
	view := int64(discordgo.PermissionViewChannel)
	apply := func(overwrites []*discordgo.PermissionOverwrite) {
		for _, o := range overwrites {
			if o.Type == discordgo.PermissionOverwriteTypeRole && o.ID == guildID {
				perms &^= o.Deny
				perms |= o.Allow
			}
		}
	}
	apply(ch.PermissionOverwrites) // a channel's own overwrites are what count, synced to its category or not
	return perms&view != 0
}

// checkAcceptRole: a role the bot can hand out (same rules as sign-up panels).
func checkAcceptRole(s *discordgo.Session, guildID string, role *discordgo.Role) error {
	if role == nil {
		return nil
	}
	facts, err := loadRoleFacts(s, guildID)
	if err != nil {
		return err
	}
	full, ok := facts.Roles[role.ID]
	if !ok {
		return userErrorf("I can't find that role.")
	}
	if why := roleProblem(full, facts); why != "" {
		return userErrorf("I can't give %s to accepted applicants: %s.", roleMention(role.ID), why)
	}
	return nil
}

// buttonEmojiText is a form's button emoji as typed ("" to clear), or a mistake.
func buttonEmojiText(text string) (string, error) {
	e, err := parseButtonEmoji(text)
	if err != nil {
		return "", userErrorf("That isn't an emoji. Type one emoji, like 🎫, or `none` to have no emoji.")
	}
	if e == nil {
		return "", nil
	}
	return strings.TrimSpace(text), nil
}

func dmWords(f *appForm) string {
	switch {
	case f.DMAccept && f.DMDecline:
		return "when accepted or declined"
	case f.DMAccept:
		return "only when accepted"
	case f.DMDecline:
		return "only when declined"
	}
	return "never"
}

// ============================================================================
// /form
// ============================================================================

func handleFormCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if i.GuildID == "" || i.Member == nil {
		respondEphemeral(s, i, "Forms only work inside a server.")
		return
	}
	if !canManageServer(i) {
		respondEphemeral(s, i, "You need Manage Server (or the staff role) to manage forms.")
		return
	}
	if appForms == nil {
		respondEphemeral(s, i, "Forms need the bot's database, which isn't available right now.")
		return
	}
	name, opts := commandOptions(i)
	// These answer with a pop-up or an interactive message, so they can't be deferred.
	switch name {
	case "header", "welcome":
		formTextBox(s, i, name, opts)
		return
	case "panel":
		formPanelMaker(s, i, opts)
		return
	case "delete":
		formDeleteConfirm(s, i, opts)
		return
	case "show":
		formShow(s, i, opts)
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

type cmdOpts = map[string]*discordgo.ApplicationCommandInteractionDataOption

func runFormSubcommand(s *discordgo.Session, i *discordgo.InteractionCreate, name string, opts cmdOpts) (string, error) {
	switch name {
	case "create":
		return formCreate(s, i, opts)
	case "list":
		return formList(i)
	}
	id, _ := optInt(opts, "form")
	form, err := needForm(i.GuildID, id)
	if err != nil {
		return "", err
	}
	switch name {
	case "edit":
		return formEdit(s, i, opts, form)
	case "post":
		return formPost(s, i, opts, form)
	case "chat":
		return formChat(s, i, opts, form)
	case "question add":
		return formQuestionAdd(opts, form)
	case "question remove":
		return formQuestionRemove(opts, form)
	case "question move":
		return formQuestionMove(opts, form)
	}
	return "", userErrorf("Unknown subcommand.")
}

func formCreate(s *discordgo.Session, i *discordgo.InteractionCreate, opts cmdOpts) (string, error) {
	name, _ := optString(opts, "name")
	name = strings.TrimSpace(name)
	if name == "" {
		return "", userErrorf("The form needs a name.")
	}
	review := optChannel(i, opts, "review_channel")
	allowPublic, _ := optBool(opts, "allow_public")
	if err := checkReviewChannel(s, i.GuildID, review.ID, allowPublic); err != nil {
		return "", err
	}
	accept := optRole(i, opts, "accept_role")
	reviewer := optRole(i, opts, "reviewer_role")
	if err := checkAcceptRole(s, i.GuildID, accept); err != nil {
		return "", err
	}
	if accept != nil && reviewer != nil && accept.ID == reviewer.ID {
		return "", userErrorf("The reviewer role can't be the role accepted applicants get.")
	}
	intro, _ := optString(opts, "intro")
	f := &appForm{
		GuildID: i.GuildID, Name: name, Intro: strings.TrimSpace(intro), ReviewChannelID: review.ID,
		Open: true, DMAccept: true, DMDecline: true, CreatedBy: i.Member.User.ID,
	}
	if accept != nil {
		f.AcceptRoleID = accept.ID
	}
	if reviewer != nil {
		f.ReviewerRoleID = reviewer.ID
	}
	id, err := appForms.CreateForm(f)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("✅ Made the form **%s** (#%d). Next:\n1. Add questions with `/form question add form:%s`.\n2. Try it with `/form show` and **👀 Try it**.\n3. Post it with `/form post`.",
		name, id, name), nil
}

func formEdit(s *discordgo.Session, i *discordgo.InteractionCreate, opts cmdOpts, f *appForm) (string, error) {
	var changed []string
	if name, ok := optString(opts, "name"); ok && strings.TrimSpace(name) != "" {
		f.Name, changed = strings.TrimSpace(name), append(changed, "name")
	}
	if intro, ok := optString(opts, "intro"); ok && strings.TrimSpace(intro) != "" {
		f.Intro, changed = strings.TrimSpace(intro), append(changed, "intro")
	}
	if ch := optChannel(i, opts, "review_channel"); ch != nil {
		allowPublic, _ := optBool(opts, "allow_public")
		if err := checkReviewChannel(s, i.GuildID, ch.ID, allowPublic); err != nil {
			return "", err
		}
		f.ReviewChannelID, changed = ch.ID, append(changed, "review channel")
	}
	reviewer := optRole(i, opts, "reviewer_role")
	if accept := optRole(i, opts, "accept_role"); accept != nil {
		if err := checkAcceptRole(s, i.GuildID, accept); err != nil {
			return "", err
		}
		staff, err := appForms.ChatRoles(f.ID)
		if err != nil {
			return "", err
		}
		staff = append(staff, f.ReviewerRoleID)
		if reviewer != nil {
			staff = append(staff, reviewer.ID)
		}
		for _, id := range staff {
			if id == accept.ID {
				return "", userErrorf("%s is a reviewer or chat staff role; accepted applicants would see every application. Pick another role.", roleMention(accept.ID))
			}
		}
		f.AcceptRoleID, changed = accept.ID, append(changed, "accept role")
	} else if remove, _ := optBool(opts, "remove_accept_role"); remove {
		f.AcceptRoleID, changed = "", append(changed, "accept role")
	}
	if reviewer != nil {
		if reviewer.ID == f.AcceptRoleID {
			return "", userErrorf("The reviewer role can't be the role accepted applicants get.")
		}
		f.ReviewerRoleID, changed = reviewer.ID, append(changed, "reviewer role")
	}
	if open, ok := optBool(opts, "open"); ok {
		f.Open, changed = open, append(changed, "open")
	}
	if v, ok := optBool(opts, "dm_accept"); ok {
		f.DMAccept, changed = v, append(changed, "dm accept")
	}
	if v, ok := optBool(opts, "dm_decline"); ok {
		f.DMDecline, changed = v, append(changed, "dm decline")
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
	return fmt.Sprintf("✅ Changed **%s**: %s. Posted Apply buttons keep working.", f.Name, strings.Join(changed, ", ")), nil
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
		waiting, err := appForms.ListEntries(i.GuildID, f.ID, entryPending)
		if err != nil {
			return "", err
		}
		questions, err := appForms.Questions(f.ID)
		if err != nil {
			return "", err
		}
		state := "🔴 closed"
		if f.Open {
			state = "🟢 open"
		}
		lines = append(lines, fmt.Sprintf("**%s** (#%d) · %s · %d question(s) · %d waiting · reviews in <#%s>",
			f.Name, f.ID, state, len(questions), len(waiting), f.ReviewChannelID))
	}
	return fitLines(lines[0], lines[1:], func(left int) string {
		return fmt.Sprintf("…and %d more. `/form show` opens any form by name.", left)
	}), nil
}

func formShow(s *discordgo.Session, i *discordgo.InteractionCreate, opts cmdOpts) {
	id, _ := optInt(opts, "form")
	embed, hasQuestions, err := formShowEmbed(s, i.GuildID, id)
	if err != nil {
		respondEphemeral(s, i, errorText("/form show", err))
		return
	}
	data := &discordgo.InteractionResponseData{
		Embeds: []*discordgo.MessageEmbed{embed}, Flags: discordgo.MessageFlagsEphemeral,
		AllowedMentions: &discordgo.MessageAllowedMentions{},
	}
	if hasQuestions {
		data.Components = []discordgo.MessageComponent{discordgo.ActionsRow{Components: []discordgo.MessageComponent{
			discordgo.Button{Label: "👀 Try it", Style: discordgo.PrimaryButton, CustomID: fmt.Sprintf("formtry:%d", id)},
		}}}
	}
	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{Type: discordgo.InteractionResponseChannelMessageWithSource, Data: data}); err != nil {
		appLogger.Warnf("Could not answer /form show: %v", err)
	}
}

func formShowEmbed(s *discordgo.Session, guildID string, id int64) (*discordgo.MessageEmbed, bool, error) {
	f, err := needForm(guildID, id)
	if err != nil {
		return nil, false, err
	}
	questions, err := appForms.Questions(id)
	if err != nil {
		return nil, false, err
	}
	role := "no role"
	if f.AcceptRoleID != "" {
		role = roleMention(f.AcceptRoleID)
	}
	reviewers := ""
	if f.ReviewerRoleID != "" {
		reviewers = " and " + roleMention(f.ReviewerRoleID)
	}
	state := "🔴 Closed"
	if f.Open {
		state = "🟢 Taking applications"
	}
	lines := []string{
		fmt.Sprintf("%s · reviews go to <#%s>", state, f.ReviewChannelID),
		"Accepted applicants get: " + role,
		"Reviewers: Manage Server" + reviewers,
		"DM to the applicant: " + dmWords(f),
	}
	if f.ChatCategoryID != "" {
		staff, err := formChatStaff(s, guildID, f)
		if err != nil {
			return nil, false, err
		}
		names := mentionAll(staff)
		if names == "" {
			names = "no one yet"
		}
		lines = append(lines, fmt.Sprintf("Chats with applicants: in <#%s>, with %s", f.ChatCategoryID, names))
	} else {
		lines = append(lines, "Chats with applicants: off (`/form chat`)")
	}
	if f.Intro != "" {
		lines = append(lines, "Intro: "+f.Intro)
	}
	if f.Welcome != "" {
		lines = append(lines, "Message in each applicant's chat:\n> "+strings.ReplaceAll(shorten(f.Welcome, 200), "\n", "\n> "))
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
		total := len(formPages(questions, f.Header != ""))
		extra := "."
		if f.Header != "" {
			extra = " (one fewer on the first, under the text at the top)."
		}
		lines = append(lines, fmt.Sprintf("\n%d question(s), shown as %d page(s) of up to %d%s", len(questions), total, formPerPage, extra))
	}
	return &discordgo.MessageEmbed{
		Title:       fmt.Sprintf("📝 %s (#%d)", f.Name, f.ID),
		Description: truncateRunes(strings.Join(lines, "\n"), 4000),
		Color:       formColor,
	}, len(questions) > 0, nil
}

// formReadyProblem is why a form can't be posted (or go on a panel) yet, if it can't.
func formReadyProblem(f *appForm) (string, error) {
	questions, err := appForms.Questions(f.ID)
	if err != nil {
		return "", err
	}
	if len(questions) == 0 && f.ChatCategoryID == "" {
		return fmt.Sprintf("**%s** has no questions. Add some with `/form question add`, or turn on chats with `/form chat` to make it a ticket that just opens a chat.", f.Name), nil
	}
	return "", nil
}

func formPost(s *discordgo.Session, i *discordgo.InteractionCreate, opts cmdOpts, f *appForm) (string, error) {
	if problem, err := formReadyProblem(f); err != nil || problem != "" {
		if err != nil {
			return "", err
		}
		return "", userErrorf("%s", problem)
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
		intro = "Press **📝 Apply** to fill in the form. Only staff see your answers."
	}
	_, err := s.ChannelMessageSendComplex(channelID, &discordgo.MessageSend{
		Embeds: []*discordgo.MessageEmbed{{
			Title: "📝 " + f.Name, Description: intro, Color: formColor,
			Footer: &discordgo.MessageEmbedFooter{Text: "Your answers go only to staff. You'll get the decision by DM."},
		}},
		Components: []discordgo.MessageComponent{discordgo.ActionsRow{Components: []discordgo.MessageComponent{
			formButton(f.ID, "apply"), formButton(f.ID, "status"),
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

func formChat(s *discordgo.Session, i *discordgo.InteractionCreate, opts cmdOpts, f *appForm) (string, error) {
	if off, _ := optBool(opts, "off"); off {
		f.ChatCategoryID = ""
		if err := appForms.UpdateForm(f); err != nil {
			return "", err
		}
		return fmt.Sprintf("✅ **%s** won't open chats any more. Chats already open stay; delete them by hand when you're done.", f.Name), nil
	}
	staffRole := optRole(i, opts, "staff_role")
	removeRole := optRole(i, opts, "remove_staff_role")
	if staffRole != nil {
		if staffRole.ID == i.GuildID {
			return "", userErrorf("@everyone can't be a chat's staff: the chats would be public.")
		}
		if staffRole.ID == f.AcceptRoleID {
			return "", userErrorf("%s is given to accepted applicants, so they'd see every chat. Pick a staff role.", roleMention(staffRole.ID))
		}
		if err := appForms.AddChatRole(f.ID, staffRole.ID); err != nil {
			return "", err
		}
	}
	if removeRole != nil {
		if err := appForms.RemoveChatRole(f.ID, removeRole.ID); err != nil {
			return "", err
		}
	}
	if cat := optChannel(i, opts, "category"); cat != nil {
		perms, err := s.UserChannelPermissions(s.State.User.ID, cat.ID)
		if err != nil {
			return "", err
		}
		need := int64(discordgo.PermissionManageChannels | discordgo.PermissionViewChannel)
		if perms&discordgo.PermissionAdministrator == 0 && perms&need != need {
			return "", userErrorf("I need Manage Channels and View Channel in that category to make chats there.")
		}
		if n := categoryChannelCount(s, i.GuildID, cat.ID); n >= categoryChannelLimit {
			return "", userErrorf("That category is full (%d channels). Pick another.", categoryChannelLimit)
		}
		f.ChatCategoryID = cat.ID
		if err := appForms.UpdateForm(f); err != nil {
			return "", err
		}
	}
	staff, err := formChatStaff(s, i.GuildID, f)
	if err != nil {
		return "", err
	}
	if f.ChatCategoryID != "" && len(staff) == 0 {
		return "⚠️ Chats are on, but no staff role would see them, so none will open. Add one with `staff_role:` (or give the form a reviewer role).", nil
	}
	if f.ChatCategoryID == "" {
		if staffRole == nil && removeRole == nil {
			return "", userErrorf("Pick a `category:` to turn chats on.")
		}
		return "Saved. Chats aren't on yet: pick a `category:` for them.", nil
	}
	return fmt.Sprintf("✅ After applying to **%s**, each applicant gets a private chat in <#%s> with %s. Only they, those roles and I can see it.",
		f.Name, f.ChatCategoryID, mentionAll(staff)), nil
}

func formQuestionAdd(opts cmdOpts, f *appForm) (string, error) {
	existing, err := appForms.Questions(f.ID)
	if err != nil {
		return "", err
	}
	if len(existing) >= formMaxQuestions {
		return "", userErrorf("A form can have at most %d questions.", formMaxQuestions)
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
	if err := appForms.AddQuestion(f.ID, q); err != nil {
		return "", err
	}
	count := len(existing) + 1
	pages := len(formPages(append(existing, q), f.Header != ""))
	return fmt.Sprintf("✅ Question %d added to **%s**: %s (%s). The form now has %d question(s) on %d page(s).",
		count, f.Name, label, kindName(kind), count, pages), nil
}

// numberedQuestion is question number (from 1) on a form.
func numberedQuestion(f *appForm, number int64) ([]formQuestion, formQuestion, error) {
	questions, err := appForms.Questions(f.ID)
	if err != nil {
		return nil, formQuestion{}, err
	}
	if number < 1 || int(number) > len(questions) {
		if len(questions) == 0 {
			return nil, formQuestion{}, userErrorf("**%s** has no questions yet.", f.Name)
		}
		return nil, formQuestion{}, userErrorf("**%s** has questions 1 to %d. `/form show` lists them.", f.Name, len(questions))
	}
	return questions, questions[number-1], nil
}

func formQuestionRemove(opts cmdOpts, f *appForm) (string, error) {
	number, _ := optInt(opts, "number")
	questions, q, err := numberedQuestion(f, number)
	if err != nil {
		return "", err
	}
	if err := appForms.RemoveQuestion(f.ID, q.ID); err != nil {
		return "", err
	}
	var order []int64
	for _, other := range questions {
		if other.ID != q.ID {
			order = append(order, other.ID)
		}
	}
	if err := appForms.ReorderQuestions(f.ID, order); err != nil {
		return "", err
	}
	return fmt.Sprintf("✅ Removed question %d (%s) from **%s**. Applications already sent keep their answers.", number, q.Label, f.Name), nil
}

func formQuestionMove(opts cmdOpts, f *appForm) (string, error) {
	number, _ := optInt(opts, "number")
	to, _ := optInt(opts, "to")
	questions, q, err := numberedQuestion(f, number)
	if err != nil {
		return "", err
	}
	if to < 1 || int(to) > len(questions) {
		return "", userErrorf("Pick a new number from 1 to %d.", len(questions))
	}
	order := moveID(questions, q.ID, int(to))
	if err := appForms.ReorderQuestions(f.ID, order); err != nil {
		return "", err
	}
	return fmt.Sprintf("✅ Moved \"%s\" to number %d on **%s**.", q.Label, to, f.Name), nil
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

// ---- /form delete: confirm with buttons --------------------------------------------------

func formDeleteConfirm(s *discordgo.Session, i *discordgo.InteractionCreate, opts cmdOpts) {
	id, _ := optInt(opts, "form")
	f, err := needForm(i.GuildID, id)
	if err != nil {
		respondEphemeral(s, i, errorText("/form delete", err))
		return
	}
	sent, err := appForms.ListEntries(i.GuildID, id, "")
	if err != nil {
		respondEphemeral(s, i, errorText("/form delete", err))
		return
	}
	err = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: fmt.Sprintf("Delete **%s**, its questions and **%d** application(s) sent to it? This can't be undone. (To stop new applications but keep them, use `/form edit open:False`.)",
				f.Name, len(sent)),
			Flags: discordgo.MessageFlagsEphemeral,
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
				content = fmt.Sprintf("Deleted **%s**. Its Apply buttons now say the form is gone.", f.Name)
			}
		}
	}
	updateMessage(s, i, content, nil)
}

// updateMessage replaces the message a button was on (used for ephemeral menus).
func updateMessage(s *discordgo.Session, i *discordgo.InteractionCreate, content string, components []discordgo.MessageComponent) {
	if components == nil {
		components = []discordgo.MessageComponent{}
	}
	err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseUpdateMessage,
		Data: &discordgo.InteractionResponseData{Content: content, Components: components, AllowedMentions: &discordgo.MessageAllowedMentions{}},
	})
	if err != nil {
		appLogger.Warnf("Could not update a message: %v", err)
	}
}

// ---- /form header and /form welcome: a pop-up with one big box ---------------------------

func formTextBox(s *discordgo.Session, i *discordgo.InteractionCreate, which string, opts cmdOpts) {
	id, _ := optInt(opts, "form")
	f, err := needForm(i.GuildID, id)
	if err != nil {
		respondEphemeral(s, i, errorText("/form "+which, err))
		return
	}
	title, label, help, current, limit := "Text at the top of "+f.Name, "What to show above the questions",
		"New lines and **bold** work. Leave it empty to remove the text.", f.Header, formHeaderMax
	if which == "welcome" {
		title, label, help, current, limit = "Chat message for "+f.Name, "Posted in the applicant's chat",
			"{member} pings them; {name} and {form} work too. Leave it empty to remove it.", f.Welcome, formWelcomeMax
	}
	required := false
	err = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseModal,
		Data: &discordgo.InteractionResponseData{
			CustomID: fmt.Sprintf("formtext:%d:%s", f.ID, which),
			Title:    truncateRunes(title, 45),
			Components: []discordgo.MessageComponent{discordgo.Label{
				Label: label, Description: help,
				Component: discordgo.TextInput{CustomID: "text", Style: discordgo.TextInputParagraph, Required: &required, MaxLength: limit, Value: current},
			}},
		},
	})
	if err != nil {
		appLogger.Warnf("Could not open the /form %s pop-up: %v", which, err)
	}
}

func handleFormTextSubmit(s *discordgo.Session, i *discordgo.InteractionCreate, customID string) {
	parts := strings.Split(customID, ":")
	id, ok := parseID(parts, 1)
	if !ok || len(parts) != 3 || (parts[2] != "header" && parts[2] != "welcome") {
		respondEphemeral(s, i, "That form has expired. Please try again.")
		return
	}
	if !canManageServer(i) {
		respondEphemeral(s, i, "You need Manage Server (or the staff role) to change forms.")
		return
	}
	text := strings.TrimSpace(modalValues(i)["text"].text())
	msg, err := saveFormText(i.GuildID, id, parts[2], text)
	if err != nil {
		msg = errorText("Saving a form's text", err)
	}
	respondEphemeral(s, i, msg)
}

func saveFormText(guildID string, id int64, which, text string) (string, error) {
	f, err := needForm(guildID, id)
	if err != nil {
		return "", err
	}
	if which == "header" {
		f.Header = truncateRunes(text, formHeaderMax)
	} else {
		f.Welcome = truncateRunes(text, formWelcomeMax)
	}
	if err := appForms.UpdateForm(f); err != nil {
		return "", err
	}
	if which == "header" {
		if text == "" {
			return fmt.Sprintf("✅ **%s** has no text at the top now.", f.Name), nil
		}
		return fmt.Sprintf("✅ Saved the text at the top of **%s**. It shows above the first questions, which leaves room for %d questions on that page. See it with `/form show` → 👀 Try it.",
			f.Name, formPerPage-1), nil
	}
	if text == "" {
		return fmt.Sprintf("✅ **%s**'s chats get only the usual message now.", f.Name), nil
	}
	var words []string
	for _, w := range formWelcomeWords {
		words = append(words, fmt.Sprintf("`%s` %s", w.Word, w.What))
	}
	off := ""
	if f.ChatCategoryID == "" {
		off = "\n⚠️ Chats are off for this form, so it won't be posted until you turn them on with `/form chat`."
	}
	return fmt.Sprintf("✅ Saved. Each applicant's chat for **%s** gets your message right after their application. In it, %s.%s",
		f.Name, strings.Join(words, ", "), off), nil
}

// ============================================================================
// /applications
// ============================================================================

func handleApplicationsCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if i.GuildID == "" || i.Member == nil {
		respondEphemeral(s, i, "Applications only work inside a server.")
		return
	}
	if !canManageServer(i) {
		respondEphemeral(s, i, "You need Manage Server (or the staff role) to see applications.")
		return
	}
	if appForms == nil {
		respondEphemeral(s, i, "Forms need the bot's database, which isn't available right now.")
		return
	}
	name, opts := commandOptions(i)
	if name == "view" {
		applicationsView(s, i, opts)
		return
	}
	if err := deferEphemeral(s, i); err != nil {
		return
	}
	msg, err := applicationsList(i, opts)
	if err != nil {
		msg = errorText("/applications list", err)
	}
	editResponse(s, i, msg)
}

func applicationsList(i *discordgo.InteractionCreate, opts cmdOpts) (string, error) {
	formID, _ := optInt(opts, "form")
	status, ok := optString(opts, "status")
	if !ok {
		status = entryPending
	}
	forms, err := appForms.ListForms(i.GuildID)
	if err != nil {
		return "", err
	}
	names := map[int64]string{}
	for _, f := range forms {
		names[f.ID] = f.Name
	}
	found, err := appForms.ListEntries(i.GuildID, formID, status)
	if err != nil {
		return "", err
	}
	if len(found) == 0 {
		_, words, _ := strings.Cut(entryStatusWords[status], " ")
		return "No applications " + words + ".", nil
	}
	shown := found
	if len(shown) > 30 {
		shown = shown[len(shown)-30:]
	}
	var lines []string
	for _, e := range shown {
		where := ""
		if e.ReviewMessageID != "" {
			where = " · [open](" + messageLink(i.GuildID, e.ReviewChannelID, e.ReviewMessageID) + ")"
		}
		votes, err := appForms.Votes(e.ID)
		if err != nil {
			return "", err
		}
		name, ok := names[e.FormID]
		if !ok {
			name = "a deleted form"
		}
		lines = append(lines, fmt.Sprintf("#%d · %s · <@%s> · <t:%d:R> · %s%s", e.ID, name, e.UserID, e.SubmittedAt.Unix(), voteTally(votes, false), where))
	}
	older := len(found) - len(shown)
	return fitLines(fmt.Sprintf("**%d application(s), %s:**", len(found), entryStatusWords[status]), lines, func(left int) string {
		return fmt.Sprintf("…and %d more. `/applications view` opens any by number.", left+older)
	}), nil
}

func applicationsView(s *discordgo.Session, i *discordgo.InteractionCreate, opts cmdOpts) {
	number, _ := optInt(opts, "number")
	fail := func(err error) { respondEphemeral(s, i, errorText("/applications view", err)) }
	e, err := appForms.GetEntry(i.GuildID, number)
	if err != nil {
		fail(err)
		return
	}
	if e == nil {
		respondEphemeral(s, i, fmt.Sprintf("There's no application #%d in this server.", number))
		return
	}
	f, err := needForm(i.GuildID, e.FormID)
	if err != nil {
		fail(err)
		return
	}
	votes, err := appForms.Votes(e.ID)
	if err != nil {
		fail(err)
		return
	}
	msg := reviewMessage(s, f, e, votes, true)
	err = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Embeds: msg.Embeds, Components: msg.Components, Files: msg.Files,
			Flags: discordgo.MessageFlagsEphemeral, AllowedMentions: &discordgo.MessageAllowedMentions{},
		},
	})
	if err != nil {
		appLogger.Warnf("Could not answer /applications view: %v", err)
	}
}

func parseID(parts []string, at int) (int64, bool) {
	if at >= len(parts) {
		return 0, false
	}
	var id int64
	if _, err := fmt.Sscanf(parts[at], "%d", &id); err != nil || id <= 0 || fmt.Sprint(id) != parts[at] {
		return 0, false
	}
	return id, true
}
