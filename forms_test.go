package main

import (
	"context"
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
)

func TestParseChoices(t *testing.T) {
	got := parseChoices(" Yes, No ,, yes, Maybe ")
	if strings.Join(got, "|") != "Yes|No|Maybe" {
		t.Fatalf("got %q", got)
	}
	if parseChoices("") != nil {
		t.Fatal("empty")
	}
}

func TestQuestionProblems(t *testing.T) {
	if p := questionProblems("Your age?", kindShort, "", nil, 0); len(p) != 0 {
		t.Fatalf("fine question: %v", p)
	}
	cases := []struct {
		label, kind, help string
		choices           []string
		max               int
		want              string
	}{
		{"x", "essay", "", nil, 0, "kind"},
		{" ", kindShort, "", nil, 0, "needs some words"},
		{strings.Repeat("a", 46), kindShort, "", nil, 0, "at most 45"},
		{"x", kindShort, strings.Repeat("h", 101), nil, 0, "help line"},
		{"x", kindChoice, "", []string{"one"}, 0, "at least two"},
		{"x", kindMulti, "", make([]string, 26), 0, "at most 25"},
		{"x", kindShort, "", []string{"a", "b"}, 0, "Leave `choices` empty"},
		{"x", kindShort, "", nil, 301, "1 to 300"},
		{"x", kindParagraph, "", nil, -1, "1 to 1000"},
	}
	for _, c := range cases {
		got := strings.Join(questionProblems(c.label, c.kind, c.help, c.choices, c.max), " ")
		if !strings.Contains(got, c.want) {
			t.Errorf("%+v: want %q in %q", c, c.want, got)
		}
	}
}

func makeQuestions(n int) []formQuestion {
	out := make([]formQuestion, n)
	for i := range out {
		out[i] = formQuestion{ID: int64(i + 1), Label: "Q", Kind: kindShort, Required: true}
	}
	return out
}

func pageSizes(pages [][]formQuestion) []int {
	out := make([]int, len(pages))
	for i, p := range pages {
		out[i] = len(p)
	}
	return out
}

func TestFormPages(t *testing.T) {
	cases := []struct {
		n      int
		header bool
		want   string
	}{
		{0, false, "[0]"}, {3, false, "[3]"}, {5, false, "[5]"}, {6, false, "[5,1]"}, {20, false, "[5,5,5,5]"},
		{0, true, "[0]"}, {4, true, "[4]"}, {5, true, "[4,1]"}, {20, true, "[4,5,5,5,1]"},
	}
	for _, c := range cases {
		if got := toString(pageSizes(formPages(makeQuestions(c.n), c.header))); got != c.want {
			t.Errorf("%d questions, header %v: got %s, want %s", c.n, c.header, got, c.want)
		}
	}
}

func toString(v any) string { b, _ := json.Marshal(v); return string(b) }

func TestQuestionLimitAndDescribe(t *testing.T) {
	q := formQuestion{Label: "Why?", Kind: kindParagraph, MaxLength: 5000}
	if q.Limit() != 1000 {
		t.Fatalf("limit = %d", q.Limit())
	}
	q.MaxLength = 200
	if q.Limit() != 200 || !strings.Contains(q.Describe(), "up to 200 characters, optional") {
		t.Fatalf("describe = %q", q.Describe())
	}
	c := formQuestion{Label: "Team", Kind: kindChoice, Choices: []string{"Red", "Blue"}, Required: true}
	if c.Describe() != "Team (Pick one; Red / Blue, required)" {
		t.Fatalf("describe = %q", c.Describe())
	}
}

func TestDraftFlow(t *testing.T) {
	qs := makeQuestions(6)
	qs[5].Kind, qs[5].Choices = kindMulti, []string{"A", "B", "C"}
	d := &formDraft{Questions: qs, Header: "Read this"}
	if d.Total() != 2 || len(d.Current()) != 4 {
		t.Fatalf("total %d, first page %d", d.Total(), len(d.Current()))
	}
	d.Record(map[int64][]string{1: {" one "}, 2: {"two"}})
	if d.Done() || len(d.Current()) != 2 {
		t.Fatal("second page")
	}
	d.Record(map[int64][]string{6: {"A", "C"}})
	if !d.Done() {
		t.Fatal("done")
	}
	saved := d.Saved()
	if len(saved) != 6 || saved[0].Answer != "one" || saved[2].Answer != "" || saved[5].Answer != "A, C" {
		t.Fatalf("saved = %+v", saved)
	}
	if back := loadAnswers(dumpAnswers(saved)); len(back) != 6 || back[5] != saved[5] {
		t.Fatalf("round trip = %+v", back)
	}
	text := formTranscript("T", "From: x", saved)
	if !strings.Contains(text, "3. Q\n(no answer)") {
		t.Fatalf("transcript = %q", text)
	}
}

func TestFillWelcomeAndChatName(t *testing.T) {
	got := fillWelcome("Hi {member} ({name}), welcome to {form}!", "<@1>", "Ann", "Mods")
	if got != "Hi <@1> (Ann), welcome to Mods!" {
		t.Fatalf("got %q", got)
	}
	if n := chatName("Ann  B.--Smith", "9"); n != "apply-ann-b-smith" {
		t.Fatalf("chat name = %q", n)
	}
	if n := chatName("!!!", "9"); n != "apply-9" {
		t.Fatalf("chat name = %q", n)
	}
}

func TestMoveID(t *testing.T) {
	qs := makeQuestions(4)
	if got := toString(moveID(qs, 1, 4)); got != "[2,3,4,1]" {
		t.Fatal(got)
	}
	if got := toString(moveID(qs, 4, 1)); got != "[4,1,2,3]" {
		t.Fatal(got)
	}
	if got := toString(moveID(qs, 2, 3)); got != "[1,3,2,4]" {
		t.Fatal(got)
	}
}

func TestFitLines(t *testing.T) {
	lines := make([]string, 100)
	for i := range lines {
		lines[i] = strings.Repeat("x", 50)
	}
	got := fitLines("head", lines, func(left int) string { return "…and more" })
	if len(got) > 2000 || !strings.HasSuffix(got, "…and more") {
		t.Fatalf("len %d", len(got))
	}
}

// ---- pop-ups -------------------------------------------------------------------------------

func TestBuildFormPage(t *testing.T) {
	qs := []formQuestion{
		{ID: 11, Label: "Name", Kind: kindShort, Required: true},
		{ID: 12, Label: "Team", Kind: kindChoice, Choices: []string{"Red", "Blue"}, Required: true, Help: "Pick a side"},
		{ID: 13, Label: "Skills", Kind: kindMulti, Choices: []string{"Go", "C#", "GDScript"}},
		{ID: 14, Label: "About you", Kind: kindParagraph},
		{ID: 15, Label: "Extra", Kind: kindShort},
	}
	d := &formDraft{FormName: "A very long form name that goes past the limit", Questions: qs, Header: "Welcome!"}
	data := buildFormPage(d, "tok")
	if data.CustomID != "formpage:tok" || len([]rune(data.Title)) > formNameMax || !strings.HasSuffix(data.Title, "(1/2)") {
		t.Fatalf("title %q id %q", data.Title, data.CustomID)
	}
	if len(data.Components) != 5 { // header + 4 questions
		t.Fatalf("components = %d", len(data.Components))
	}
	raw, err := json.Marshal(data.Components)
	if err != nil {
		t.Fatal(err)
	}
	js := string(raw)
	for _, want := range []string{`"type":10`, `"type":18`, `"label":"Team"`, `"description":"Pick a side"`, `"custom_id":"q12"`, `"max_values":3`, `"min_values":0`, `"style":2`} {
		if !strings.Contains(js, want) {
			t.Errorf("missing %s in %s", want, js)
		}
	}
}

func TestModalValuesAndPageValues(t *testing.T) {
	payload := `{"type":5,"data":{"custom_id":"formpage:tok","components":[
		{"type":10,"content":"Welcome!"},
		{"type":18,"component":{"type":4,"custom_id":"q11","value":"Ann"}},
		{"type":18,"component":{"type":3,"custom_id":"q12","values":["1"]}},
		{"type":18,"component":{"type":3,"custom_id":"q13","values":["0","2","9"]}},
		{"type":1,"components":[{"type":4,"custom_id":"q14","value":"old style"}]}
	]}}`
	var in discordgo.Interaction
	if err := json.Unmarshal([]byte(payload), &in); err != nil {
		t.Fatalf("a submitted pop-up with labels must decode: %v", err)
	}
	fields := modalValues(&discordgo.InteractionCreate{Interaction: &in})
	if fields["q11"].text() != "Ann" || fields["q14"].text() != "old style" {
		t.Fatalf("fields = %+v", fields)
	}
	qs := []formQuestion{
		{ID: 11, Kind: kindShort},
		{ID: 12, Kind: kindChoice, Choices: []string{"Red", "Blue"}},
		{ID: 13, Kind: kindMulti, Choices: []string{"Go", "C#", "GDScript"}},
	}
	values := pageValues(qs, fields)
	if toString(values[12]) != `["Blue"]` || toString(values[13]) != `["Go","GDScript"]` {
		t.Fatalf("values = %v", values)
	}
}

func TestDraftTokens(t *testing.T) {
	token := saveDraft(&formDraft{UserID: "u1"})
	if takeDraft(token, "someone-else") != nil {
		t.Fatal("another member got the draft")
	}
	if takeDraft(token, "u1") == nil {
		t.Fatal("owner lost the draft")
	}
	dropDraft(token)
	if takeDraft(token, "u1") != nil {
		t.Fatal("dropped draft still there")
	}
}

// ---- review --------------------------------------------------------------------------------

var customIDRe = regexp.MustCompile(`^[a-z]+:\d+:[a-z]+$`)

func TestBuildReviewMessage(t *testing.T) {
	f := &appForm{ID: 3, Name: "Mods"}
	answers := []formAnswer{{Question: "Why?", Answer: strings.Repeat("long ", 400)}, {Question: "Age", Answer: ""}}
	e := &appEntry{ID: 9, FormID: 3, UserID: "175928847299117063", Answers: dumpAnswers(answers), Status: entryPending}
	member := &discordgo.Member{User: &discordgo.User{Username: "ann"}, JoinedAt: time.Now().Add(-time.Hour)}
	votes := []appVote{{UserID: "1", Vote: "yes"}, {UserID: "2", Vote: "yes"}, {UserID: "3", Vote: "no"}}

	msg := buildReviewMessage(f, e, member, votes, true)
	embed := msg.Embeds[0]
	if !strings.Contains(embed.Description, "(ann)") || !strings.Contains(embed.Description, "Account made") {
		t.Fatalf("description = %q", embed.Description)
	}
	if len(msg.Files) != 1 || embed.Footer == nil {
		t.Fatal("a long answer should come with the full application as a file")
	}
	if embed.Fields[1].Value != "*(no answer)*" {
		t.Fatalf("empty answer = %q", embed.Fields[1].Value)
	}
	tally := embed.Fields[len(embed.Fields)-1].Value
	if !strings.HasPrefix(tally, "👍 2 · 👎 1 · 🤷 0") || !strings.Contains(tally, "<@1>, <@2>") {
		t.Fatalf("tally = %q", tally)
	}
	if len(msg.Components) != 2 {
		t.Fatalf("rows = %d", len(msg.Components))
	}
	for _, row := range msg.Components {
		for _, c := range row.(discordgo.ActionsRow).Components {
			id := c.(discordgo.Button).CustomID
			if !customIDRe.MatchString(id) || !strings.HasPrefix(id, "application:9:") {
				t.Errorf("custom id %q", id)
			}
		}
	}
	yes := msg.Components[1].(discordgo.ActionsRow).Components[0].(discordgo.Button)
	if yes.Label != "👍 Yes · 2" {
		t.Fatalf("vote button = %q", yes.Label)
	}

	applicantCopy := buildReviewMessage(f, e, nil, nil, false)
	if len(applicantCopy.Components) != 0 || strings.Contains(toString(applicantCopy.Embeds), "Staff votes") {
		t.Fatal("the applicant's copy must not show votes or buttons")
	}
	if !strings.Contains(applicantCopy.Embeds[0].Description, "has left the server") {
		t.Fatal("missing member")
	}

	e.Status, e.DecidedBy, e.Note = entryAccepted, "5", "Welcome aboard"
	decided := buildReviewMessage(f, e, member, votes, true)
	if len(decided.Components) != 0 || decided.Embeds[0].Color != colorAccepted || !strings.Contains(decided.Embeds[0].Description, "Welcome aboard") {
		t.Fatal("a decided application has no buttons and shows the decision")
	}
}

func TestFormPanelButtons(t *testing.T) {
	var forms []appForm
	for i := 1; i <= 7; i++ {
		forms = append(forms, appForm{ID: int64(i), Name: "Form", Emoji: "🎫"})
	}
	rows := formPanelButtons(forms)
	if len(rows) != 2 {
		t.Fatalf("rows = %d", len(rows))
	}
	first := rows[0].(discordgo.ActionsRow).Components[0].(discordgo.Button)
	if first.CustomID != "form:1:apply" || first.Emoji == nil || first.Label != "Form" {
		t.Fatalf("button = %+v", first)
	}
	last := rows[1].(discordgo.ActionsRow).Components
	if last[len(last)-1].(discordgo.Button).CustomID != "forms:mine" {
		t.Fatal("My applications goes last")
	}
}

// ---- storage -------------------------------------------------------------------------------

func exerciseFormsStore(t *testing.T, store formsStore, guild string) {
	t.Helper()
	id, err := store.CreateForm(&appForm{GuildID: guild, Name: "Mods", ReviewChannelID: "c1", Open: true, DMAccept: true, CreatedBy: "u"})
	if err != nil {
		t.Fatal(err)
	}
	if f, _ := store.GetForm("other", id); f != nil {
		t.Fatal("form visible from another server")
	}
	f, err := store.GetForm(guild, id)
	if err != nil || f == nil || !f.Open || f.DMDecline {
		t.Fatalf("get: %+v %v", f, err)
	}
	f.Header, f.Emoji, f.ChatCategoryID = "Read me", "🎫", "cat"
	if err := store.UpdateForm(f); err != nil {
		t.Fatal(err)
	}
	if list, _ := store.ListForms(guild); len(list) != 1 || list[0].Header != "Read me" || list[0].ChatCategoryID != "cat" {
		t.Fatalf("list = %+v", list)
	}

	for _, label := range []string{"A", "B", "C"} {
		if err := store.AddQuestion(id, formQuestion{Label: label, Kind: kindChoice, Choices: []string{"x", "y"}, Required: true}); err != nil {
			t.Fatal(err)
		}
	}
	qs, _ := store.Questions(id)
	if len(qs) != 3 || qs[0].Label != "A" || len(qs[0].Choices) != 2 {
		t.Fatalf("questions = %+v", qs)
	}
	if err := store.ReorderQuestions(id, moveID(qs, qs[0].ID, 3)); err != nil {
		t.Fatal(err)
	}
	if err := store.RemoveQuestion(id, qs[1].ID); err != nil {
		t.Fatal(err)
	}
	qs, _ = store.Questions(id)
	if len(qs) != 2 || qs[0].Label != "C" || qs[1].Label != "A" {
		t.Fatalf("after move and remove = %+v", qs)
	}

	_ = store.AddChatRole(id, "r1")
	_ = store.AddChatRole(id, "r1")
	_ = store.AddChatRole(id, "r2")
	_ = store.RemoveChatRole(id, "r1")
	if roles, _ := store.ChatRoles(id); toString(roles) != `["r2"]` {
		t.Fatalf("chat roles = %v", roles)
	}

	entry, err := store.Submit(&appEntry{FormID: id, GuildID: guild, UserID: "m1", Answers: `[{"question":"A","answer":"x"}]`})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Submit(&appEntry{FormID: id, GuildID: guild, UserID: "m1", Answers: "[]"}); err != errAlreadyExists {
		t.Fatalf("second waiting application: %v", err)
	}
	_ = store.SetReviewMessage(entry, "c1", "msg1")
	_ = store.SetChat(entry, "chat1")
	e, _ := store.GetEntry(guild, entry)
	if e == nil || e.Status != entryPending || e.ReviewMessageID != "msg1" || loadAnswers(e.Answers)[0].Answer != "x" {
		t.Fatalf("entry = %+v", e)
	}

	_ = store.SetVote(entry, "s1", "yes")
	_ = store.SetVote(entry, "s2", "no")
	_ = store.SetVote(entry, "s1", "unsure")
	_ = store.SetVote(entry, "s2", "")
	if votes, _ := store.Votes(entry); len(votes) != 1 || votes[0].Vote != "unsure" {
		t.Fatalf("votes = %+v", votes)
	}

	if ok, err := store.Decide(entry, entryAccepted, "s1", "nice"); !ok || err != nil {
		t.Fatalf("decide: %v %v", ok, err)
	}
	if ok, _ := store.Decide(entry, entryDeclined, "s2", ""); ok {
		t.Fatal("decided twice")
	}
	if waiting, _ := store.ListEntries(guild, id, entryPending); len(waiting) != 0 {
		t.Fatal("nothing should be waiting")
	}
	second, err := store.Submit(&appEntry{FormID: id, GuildID: guild, UserID: "m1", Answers: "[]"})
	if err != nil {
		t.Fatalf("applying again after a decision: %v", err)
	}
	if latest, _ := store.LatestEntry(id, "m1"); latest == nil || latest.ID != second {
		t.Fatalf("latest = %+v", latest)
	}
	if chats, _ := store.EarlierChats(id, "m1"); toString(chats) != `["chat1"]` {
		t.Fatalf("earlier chats = %v", chats)
	}
	if all, _ := store.ListEntries(guild, 0, ""); len(all) != 2 {
		t.Fatalf("all entries = %d", len(all))
	}

	if err := store.DeleteForm(id); err != nil {
		t.Fatal(err)
	}
	if f, _ := store.GetForm(guild, id); f != nil {
		t.Fatal("form not deleted")
	}
	if e, _ := store.GetEntry(guild, entry); e != nil {
		t.Fatal("applications not deleted with the form")
	}
}

func TestMemFormsStore(t *testing.T) {
	exerciseFormsStore(t, newMemFormsStore(), "g1")
}

func TestPostgresFormsStore(t *testing.T) {
	conn := strings.TrimSpace(os.Getenv("POSTGRES_CON_STRING"))
	if conn == "" {
		t.Skip("POSTGRES_CON_STRING not set")
	}
	db, err := openPostgres(conn)
	if err != nil {
		t.Skipf("Postgres unavailable: %v", err)
	}
	defer db.Close()
	store, err := newPostgresFormsStore(db)
	if err != nil {
		t.Fatal(err)
	}
	guild := "test-" + newMarketingID()
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DELETE FROM `+tableForms+` WHERE guild_id = $1`, guild)
	})
	exerciseFormsStore(t, store, guild)
}

// ---- commands ------------------------------------------------------------------------------

// Discord refuses the whole command list if any name or description is too long, so check them all.
func checkOptionLimits(t *testing.T, path string, opts []*discordgo.ApplicationCommandOption) {
	if len(opts) > 25 {
		t.Errorf("%s has %d options (Discord allows 25)", path, len(opts))
	}
	for _, o := range opts {
		where := path + " " + o.Name
		if len(o.Name) > 32 || o.Name != strings.ToLower(o.Name) {
			t.Errorf("%s: bad name", where)
		}
		if n := len([]rune(o.Description)); n == 0 || n > 100 {
			t.Errorf("%s: description is %d characters", where, n)
		}
		for _, c := range o.Choices {
			if len([]rune(c.Name)) > 100 {
				t.Errorf("%s: choice %q too long", where, c.Name)
			}
		}
		checkOptionLimits(t, where, o.Options)
	}
}

func TestCommandsFitDiscordLimits(t *testing.T) {
	m := NewSlashCommandManager(&LoggerAdapter{})
	CommandManager = m
	t.Cleanup(func() { CommandManager = nil })
	if err := registerAllCommands(); err != nil {
		t.Fatal(err)
	}
	names := map[string]*discordgo.ApplicationCommand{}
	for _, c := range m.BuildCommandSlice() {
		names[c.Name] = c
		if n := len([]rune(c.Description)); n == 0 || n > 100 {
			t.Errorf("/%s description is %d characters", c.Name, n)
		}
		checkOptionLimits(t, "/"+c.Name, c.Options)
	}
	for _, name := range []string{"panel", "form", "applications"} {
		c, ok := names[name]
		if !ok {
			t.Fatalf("/%s not registered", name)
		}
		if c.DefaultMemberPermissions == nil || *c.DefaultMemberPermissions != discordgo.PermissionManageGuild {
			t.Errorf("/%s should default to Manage Server", name)
		}
	}
	routesMu.RLock()
	defer routesMu.RUnlock()
	for _, prefix := range []string{"signup", "form", "forms", "formtry", "formnext", "formdel", "formpanel", "application"} {
		if componentRoutes[prefix] == nil {
			t.Errorf("no button route for %q", prefix)
		}
	}
	for _, prefix := range []string{"formpage", "formtext", "appnote"} {
		if modalRoutes[prefix] == nil {
			t.Errorf("no pop-up route for %q", prefix)
		}
	}
}

func TestCommandOptionsSubcommandGroup(t *testing.T) {
	data := `{"type":2,"data":{"name":"form","options":[{"type":2,"name":"question","options":[{"type":1,"name":"add","options":[{"type":4,"name":"form","value":3},{"type":3,"name":"question","value":"Age?"}]}]}]}}`
	var in discordgo.Interaction
	if err := json.Unmarshal([]byte(data), &in); err != nil {
		t.Fatal(err)
	}
	name, opts := commandOptions(&discordgo.InteractionCreate{Interaction: &in})
	if name != "question add" {
		t.Fatalf("name = %q", name)
	}
	if id, _ := optInt(opts, "form"); id != 3 {
		t.Fatalf("form = %d", id)
	}
	if q, _ := optString(opts, "question"); q != "Age?" {
		t.Fatalf("question = %q", q)
	}
}
