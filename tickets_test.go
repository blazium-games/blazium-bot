package main

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
)

func toString(v any) string { b, _ := json.Marshal(v); return string(b) }

// ---- intake rules --------------------------------------------------------------------------

func TestParseChoices(t *testing.T) {
	if got := parseChoices(" Yes, No ,, yes, Maybe "); strings.Join(got, "|") != "Yes|No|Maybe" {
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
		if got := strings.Join(questionProblems(c.label, c.kind, c.help, c.choices, c.max), " "); !strings.Contains(got, c.want) {
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
		var sizes []int
		for _, p := range formPages(makeQuestions(c.n), c.header) {
			sizes = append(sizes, len(p))
		}
		if got := toString(sizes); got != c.want {
			t.Errorf("%d questions, header %v: got %s, want %s", c.n, c.header, got, c.want)
		}
	}
}

func TestQuestionLimitAndDescribe(t *testing.T) {
	q := formQuestion{Label: "Why?", Kind: kindParagraph, MaxLength: 5000}
	if q.Limit() != 1000 {
		t.Fatalf("limit = %d", q.Limit())
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
}

func TestNamesAndWelcome(t *testing.T) {
	if got := fillWelcome("Hi {member} ({name}), this is {type}!", "<@1>", "Ann", "Support"); got != "Hi <@1> (Ann), this is Support!" {
		t.Fatalf("welcome = %q", got)
	}
	if n := ticketChannelName("ticket", 42, "Ann  B.--Smith"); n != "ticket-0042-ann-b-smith" {
		t.Fatalf("name = %q", n)
	}
	if n := ticketChannelName("closed", 12345, "!!!"); n != "closed-12345" {
		t.Fatalf("name = %q", n)
	}
	if n := cleanChannelName("  Refund for Order #12 "); n != "refund-for-order-12" {
		t.Fatalf("clean = %q", n)
	}
}

func TestMoveID(t *testing.T) {
	qs := makeQuestions(4)
	for _, c := range []struct {
		id   int64
		to   int
		want string
	}{{1, 4, "[2,3,4,1]"}, {4, 1, "[4,1,2,3]"}, {2, 3, "[1,3,2,4]"}} {
		if got := toString(moveID(qs, c.id, c.to)); got != c.want {
			t.Errorf("move %d to %d: %s", c.id, c.to, got)
		}
	}
}

// ---- pop-ups -------------------------------------------------------------------------------

func TestBuildIntakePage(t *testing.T) {
	qs := []formQuestion{
		{ID: 11, Label: "Order number", Kind: kindShort, Required: true},
		{ID: 12, Label: "Platform", Kind: kindChoice, Choices: []string{"Windows", "Linux"}, Required: true, Help: "Where it happens"},
		{ID: 13, Label: "Affected", Kind: kindMulti, Choices: []string{"Editor", "Export", "Runtime"}},
		{ID: 14, Label: "What happened?", Kind: kindParagraph},
		{ID: 15, Label: "Extra", Kind: kindShort},
	}
	d := &formDraft{FormName: "A very long ticket type name that goes past it", Questions: qs, Header: "Before you start"}
	data := buildIntakePage(d, "tok")
	if data.CustomID != "tktpage:tok" || len([]rune(data.Title)) > formNameMax || !strings.HasSuffix(data.Title, "(1/2)") {
		t.Fatalf("title %q id %q", data.Title, data.CustomID)
	}
	if len(data.Components) != 5 { // header + 4 questions
		t.Fatalf("components = %d", len(data.Components))
	}
	js := toString(data.Components)
	for _, want := range []string{`"type":10`, `"type":18`, `"label":"Platform"`, `"description":"Where it happens"`, `"custom_id":"q12"`, `"max_values":3`, `"min_values":0`, `"style":2`} {
		if !strings.Contains(js, want) {
			t.Errorf("missing %s in %s", want, js)
		}
	}
}

func TestModalValuesAndPageValues(t *testing.T) {
	payload := `{"type":5,"data":{"custom_id":"tktpage:tok","components":[
		{"type":10,"content":"Before you start"},
		{"type":18,"component":{"type":4,"custom_id":"q11","value":"A-100"}},
		{"type":18,"component":{"type":3,"custom_id":"q12","values":["1"]}},
		{"type":18,"component":{"type":3,"custom_id":"q13","values":["0","2","9"]}},
		{"type":1,"components":[{"type":4,"custom_id":"q14","value":"old style"}]}
	]}}`
	var in discordgo.Interaction
	if err := json.Unmarshal([]byte(payload), &in); err != nil {
		t.Fatalf("a submitted pop-up with labels must decode: %v", err)
	}
	fields := modalValues(&discordgo.InteractionCreate{Interaction: &in})
	if fields["q11"].text() != "A-100" || fields["q14"].text() != "old style" {
		t.Fatalf("fields = %+v", fields)
	}
	qs := []formQuestion{
		{ID: 11, Kind: kindShort},
		{ID: 12, Kind: kindChoice, Choices: []string{"Windows", "Linux"}},
		{ID: 13, Kind: kindMulti, Choices: []string{"Editor", "Export", "Runtime"}},
	}
	values := pageValues(qs, fields)
	if toString(values[12]) != `["Linux"]` || toString(values[13]) != `["Editor","Runtime"]` {
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

// ---- messages ------------------------------------------------------------------------------

func TestOpeningMessage(t *testing.T) {
	tt := &ticketType{ID: 2, Name: "Support", Emoji: "🎫", PingStaff: false}
	answers := []formAnswer{{Question: "What happened?", Answer: strings.Repeat("long ", 400)}, {Question: "Order", Answer: ""}}
	tk := &ticket{ID: 9, Number: 42, UserID: "175928847299117063", Answers: dumpAnswers(answers)}
	member := &discordgo.Member{User: &discordgo.User{ID: tk.UserID, Username: "ann"}, JoinedAt: time.Now().Add(-time.Hour)}
	msg := openingMessage(tt, tk, []string{"500"}, member)
	if !strings.Contains(msg.Embeds[0].Title, "#0042") || !strings.Contains(msg.Content, "<@&500>") {
		t.Fatalf("message = %+v", msg)
	}
	if len(msg.AllowedMentions.Roles) != 0 || toString(msg.AllowedMentions.Users) != `["175928847299117063"]` {
		t.Fatal("only the member is pinged unless ping_staff is on")
	}
	if len(msg.Files) != 1 || msg.Embeds[0].Fields[1].Value != "*(no answer)*" {
		t.Fatal("long answers come with a file; empty ones say so")
	}
	row := msg.Components[0].(discordgo.ActionsRow)
	if row.Components[0].(discordgo.Button).CustomID != "tkt:9:claim" || row.Components[1].(discordgo.Button).CustomID != "tkt:9:close" {
		t.Fatalf("controls = %+v", row)
	}
	tt.PingStaff = true
	if msg := openingMessage(tt, tk, []string{"500"}, member); toString(msg.AllowedMentions.Roles) != `["500"]` {
		t.Fatal("ping_staff pings the staff roles")
	}
	if msg := openingMessage(tt, &ticket{ID: 1, Number: 1, UserID: "1"}, []string{"500"}, nil); len(msg.Embeds[0].Fields) != 0 || len(msg.Files) != 0 {
		t.Fatal("no questions, no fields")
	}
}

func TestTicketPanelButtons(t *testing.T) {
	var types []ticketType
	for i := 1; i <= 7; i++ {
		types = append(types, ticketType{ID: int64(i), Name: "Support", Emoji: "🎫"})
	}
	rows := ticketPanelButtons(types)
	if len(rows) != 2 {
		t.Fatalf("rows = %d", len(rows))
	}
	first := rows[0].(discordgo.ActionsRow).Components[0].(discordgo.Button)
	if first.CustomID != "ticket:1:open" || first.Emoji == nil || first.Label != "Support" {
		t.Fatalf("button = %+v", first)
	}
	last := rows[1].(discordgo.ActionsRow).Components
	if last[len(last)-1].(discordgo.Button).CustomID != "tickets:mine" {
		t.Fatal("My tickets goes last")
	}
}

func TestTicketOverwrites(t *testing.T) {
	ow := ticketOverwrites("g", "bot", "u", []string{"staff"})
	if len(ow) != 4 || ow[0].ID != "g" || ow[0].Deny != discordgo.PermissionViewChannel {
		t.Fatalf("overwrites = %+v", ow)
	}
	if ow[2].ID != "u" || ow[2].Allow&discordgo.PermissionSendMessages == 0 || ow[3].Type != discordgo.PermissionOverwriteTypeRole {
		t.Fatalf("overwrites = %+v", ow)
	}
}

func TestRenderTranscript(t *testing.T) {
	closed := time.Date(2026, 11, 25, 5, 0, 0, 0, time.UTC)
	tk := &ticket{Number: 7, UserID: "u1", OpenedAt: closed.Add(-time.Hour), ClosedBy: "s1", ClosedAt: &closed, CloseReason: "Fixed",
		Answers: dumpAnswers([]formAnswer{{Question: "Order", Answer: "A-1"}})}
	messages := []*discordgo.Message{
		{Author: &discordgo.User{Username: "ann"}, Content: "hello\nthere", Timestamp: closed.Add(-50 * time.Minute)},
		{Author: &discordgo.User{Username: "blazium", Bot: true}, Embeds: []*discordgo.MessageEmbed{{Title: "Support", Description: "opened"}}, Timestamp: closed.Add(-55 * time.Minute)},
		{Author: &discordgo.User{Username: "ann"}, Attachments: []*discordgo.MessageAttachment{{Filename: "log.txt", URL: "https://cdn.example/log.txt"}}, Timestamp: closed.Add(-40 * time.Minute)},
	}
	text := renderTranscript(&ticketType{Name: "Support"}, tk, "ann", messages)
	for _, want := range []string{"Support ticket #0007", "Reason: Fixed", "1. Order\n   A-1", "Messages (3):", "ann: hello\n    there",
		"blazium [bot]:\n    [embed] Support | opened", "[file] log.txt https://cdn.example/log.txt", "2026-11-25 04:10"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
}

func TestMissingContent(t *testing.T) {
	member := &discordgo.User{ID: "u"}
	bot := &discordgo.User{ID: "bot"}
	if !missingContent([]*discordgo.Message{{Author: member}, {Author: bot, Content: "hi"}}, "bot") {
		t.Fatal("member messages without text mean the intent is off")
	}
	if missingContent([]*discordgo.Message{{Author: member, Content: "hi"}, {Author: member}}, "bot") {
		t.Fatal("some text means it's on")
	}
	if missingContent([]*discordgo.Message{{Author: bot}}, "bot") {
		t.Fatal("no member messages, nothing to say")
	}
}

func TestCloseSummary(t *testing.T) {
	tk := &ticket{Number: 3, UserID: "u", ClosedBy: "s", ClaimedBy: "c", CloseReason: "Done", OpenedAt: time.Now()}
	e := closeSummary(&ticketType{Name: "Support"}, tk, 12)
	for _, want := range []string{"<@u>", "<@s>", "<@c>", "Done", "**Messages:** 12"} {
		if !strings.Contains(e.Description, want) {
			t.Errorf("missing %q in %q", want, e.Description)
		}
	}
}

// ---- storage -------------------------------------------------------------------------------

func exerciseTicketsStore(t *testing.T, store ticketsStore, guild string) {
	t.Helper()
	id, err := store.CreateType(&ticketType{GuildID: guild, Name: "Support", CategoryID: "cat", LogChannelID: "log", Open: true, MaxOpen: 1, DMTranscript: true, CreatedBy: "u"})
	if err != nil {
		t.Fatal(err)
	}
	if tt, _ := store.GetType("other", id); tt != nil {
		t.Fatal("type visible from another server")
	}
	tt, err := store.GetType(guild, id)
	if err != nil || tt == nil || !tt.Open || tt.PingStaff || tt.MaxOpen != 1 {
		t.Fatalf("get: %+v %v", tt, err)
	}
	tt.Header, tt.Emoji, tt.PingStaff, tt.MaxOpen = "Read me", "🎫", true, 3
	if err := store.UpdateType(tt); err != nil {
		t.Fatal(err)
	}
	if list, _ := store.ListTypes(guild); len(list) != 1 || list[0].Header != "Read me" || !list[0].PingStaff || list[0].MaxOpen != 3 {
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
	if qs, _ = store.Questions(id); len(qs) != 2 || qs[0].Label != "C" || qs[1].Label != "A" {
		t.Fatalf("after move and remove = %+v", qs)
	}

	_ = store.AddStaffRole(id, "r1")
	_ = store.AddStaffRole(id, "r1")
	_ = store.AddStaffRole(id, "r2")
	_ = store.RemoveStaffRole(id, "r1")
	if roles, _ := store.StaffRoles(id); toString(roles) != `["r2"]` {
		t.Fatalf("staff = %v", roles)
	}

	first, n1, err := store.CreateTicket(&ticket{GuildID: guild, TypeID: id, UserID: "m1", Answers: `[{"question":"A","answer":"x"}]`})
	if err != nil {
		t.Fatal(err)
	}
	second, n2, err := store.CreateTicket(&ticket{GuildID: guild, TypeID: id, UserID: "m2"})
	if err != nil || n2 != n1+1 {
		t.Fatalf("numbers %d then %d: %v", n1, n2, err)
	}
	_ = store.SetTicketChannel(first, "ch1")
	_ = store.SetClaim(first, "s1")
	got, _ := store.TicketByChannel("ch1")
	if got == nil || got.ID != first || got.ClaimedBy != "s1" || got.Status != ticketOpen || loadAnswers(got.Answers)[0].Answer != "x" {
		t.Fatalf("by channel = %+v", got)
	}
	if none, _ := store.TicketByChannel("nope"); none != nil {
		t.Fatal("unknown channel")
	}
	if open, _ := store.OpenTicketsOf(id, "m1"); len(open) != 1 {
		t.Fatalf("open of m1 = %d", len(open))
	}

	if ok, err := store.Close(first, "s1", "fixed"); !ok || err != nil {
		t.Fatalf("close: %v %v", ok, err)
	}
	if ok, _ := store.Close(first, "s2", ""); ok {
		t.Fatal("closed twice")
	}
	closed, _ := store.GetTicket(guild, first)
	if closed.Status != ticketClosed || closed.CloseReason != "fixed" || closed.ClosedAt == nil {
		t.Fatalf("closed = %+v", closed)
	}
	if open, _ := store.OpenTicketsOf(id, "m1"); len(open) != 0 {
		t.Fatal("a closed ticket isn't open")
	}
	if ok, _ := store.Reopen(first); !ok {
		t.Fatal("reopen")
	}
	if ok, _ := store.Reopen(first); ok {
		t.Fatal("reopened an open ticket")
	}
	_, _ = store.Close(first, "s1", "")
	_ = store.MarkDeleted(first)
	if list, _ := store.ListTickets(guild, 0, ticketDeleted); len(list) != 1 || list[0].ID != first {
		t.Fatalf("deleted = %+v", list)
	}
	if list, _ := store.ListTickets(guild, id, ""); len(list) != 2 || list[0].ID != second {
		t.Fatalf("all, newest first = %+v", list)
	}
	if err := store.DeleteTicket(second); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteType(id); err != nil {
		t.Fatal(err)
	}
	if tt, _ := store.GetType(guild, id); tt != nil {
		t.Fatal("type not deleted")
	}
	if tk, _ := store.GetTicket(guild, first); tk != nil {
		t.Fatal("ticket records not deleted with the type")
	}
}

func TestMemTicketsStore(t *testing.T) {
	exerciseTicketsStore(t, newMemTicketsStore(), "g1")
}

func TestPostgresTicketsStore(t *testing.T) {
	conn := strings.TrimSpace(os.Getenv("POSTGRES_CON_STRING"))
	if conn == "" {
		t.Skip("POSTGRES_CON_STRING not set")
	}
	db, err := openPostgres(conn)
	if err != nil {
		t.Skipf("Postgres unavailable: %v", err)
	}
	defer db.Close()
	store, err := newPostgresTicketsStore(db)
	if err != nil {
		t.Fatal(err)
	}
	guild := "test-" + newMarketingID()
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DELETE FROM `+tableTicketTypes+` WHERE guild_id = $1`, guild)
	})
	exerciseTicketsStore(t, store, guild)
}

// ---- commands ------------------------------------------------------------------------------

// Discord refuses the whole command list if any name or description is too long, so check them all.
func checkOptionLimits(t *testing.T, path string, opts []*discordgo.ApplicationCommandOption) {
	if len(opts) > 25 {
		t.Errorf("%s has %d options (Discord allows 25)", path, len(opts))
	}
	requiredDone := false
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
		// Discord wants required options before optional ones.
		if o.Type != discordgo.ApplicationCommandOptionSubCommand && o.Type != discordgo.ApplicationCommandOptionSubCommandGroup {
			if !o.Required {
				requiredDone = true
			} else if requiredDone {
				t.Errorf("%s: required option after an optional one", where)
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
	byName := map[string]*discordgo.ApplicationCommand{}
	for _, c := range m.BuildCommandSlice() {
		byName[c.Name] = c
		if n := len([]rune(c.Description)); n == 0 || n > 100 {
			t.Errorf("/%s description is %d characters", c.Name, n)
		}
		checkOptionLimits(t, "/"+c.Name, c.Options)
	}
	for _, name := range []string{"panel", "tickettype"} {
		if c := byName[name]; c == nil || c.DefaultMemberPermissions == nil || *c.DefaultMemberPermissions != discordgo.PermissionManageGuild {
			t.Errorf("/%s should exist and default to Manage Server", name)
		}
	}
	if c := byName["ticket"]; c == nil || c.DefaultMemberPermissions != nil {
		t.Error("/ticket should be visible to everyone (openers can close their ticket)")
	}
	routesMu.RLock()
	defer routesMu.RUnlock()
	for _, prefix := range []string{"signup", "ticket", "tickets", "tkttry", "tktnext", "tkt", "tktdel", "tktpanel"} {
		if componentRoutes[prefix] == nil {
			t.Errorf("no button route for %q", prefix)
		}
	}
	for _, prefix := range []string{"tktpage", "tkttext", "tktclose"} {
		if modalRoutes[prefix] == nil {
			t.Errorf("no pop-up route for %q", prefix)
		}
	}
}

func TestCommandOptionsSubcommandGroup(t *testing.T) {
	data := `{"type":2,"data":{"name":"tickettype","options":[{"type":2,"name":"question","options":[{"type":1,"name":"add","options":[{"type":4,"name":"type","value":3},{"type":3,"name":"question","value":"Order?"}]}]}]}}`
	var in discordgo.Interaction
	if err := json.Unmarshal([]byte(data), &in); err != nil {
		t.Fatal(err)
	}
	name, opts := commandOptions(&discordgo.InteractionCreate{Interaction: &in})
	if name != "question add" {
		t.Fatalf("name = %q", name)
	}
	if id, _ := optInt(opts, "type"); id != 3 {
		t.Fatalf("type = %d", id)
	}
}
