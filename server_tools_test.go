package main

import (
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
)

func TestParseDuration(t *testing.T) {
	good := map[string]time.Duration{
		"10m": 10 * time.Minute, "1h30m": 90 * time.Minute, "2d": 48 * time.Hour, "1w": 7 * 24 * time.Hour,
		"45": 45 * time.Minute, "90s": 90 * time.Second, "1 hour": time.Hour, "3 Days": 72 * time.Hour,
	}
	for in, want := range good {
		got, err := parseDuration(in)
		if err != nil || got != want {
			t.Errorf("parseDuration(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "abc", "10x", "m10", "9999w"} {
		if _, err := parseDuration(bad); err == nil {
			t.Errorf("parseDuration(%q) should fail", bad)
		}
	}
}

func TestFormatDuration(t *testing.T) {
	cases := map[time.Duration]string{
		0: "0s", 30 * time.Second: "30s", 90 * time.Minute: "1h 30m", 50 * time.Hour: "2d 2h", 15 * 24 * time.Hour: "2w 1d",
	}
	for in, want := range cases {
		if got := formatDuration(in); got != want {
			t.Errorf("formatDuration(%v) = %q; want %q", in, got, want)
		}
	}
}

func TestModerationProblem(t *testing.T) {
	f := &guildRoleFacts{GuildID: "g", OwnerID: "owner", BotTop: 10, Roles: map[string]*discordgo.Role{
		"admin": {ID: "admin", Position: 12}, "mod": {ID: "mod", Position: 8}, "member": {ID: "member", Position: 2},
	}}
	base := moderationTarget{ModeratorID: "m", ModeratorRoles: []string{"mod"}, TargetID: "t", TargetRoles: []string{"member"}, BotID: "bot"}
	if why := moderationProblem(base, f); why != "" {
		t.Fatalf("a mod should be able to act on a member: %s", why)
	}
	check := func(name string, change func(*moderationTarget), want string) {
		t.Helper()
		target := base
		change(&target)
		if why := moderationProblem(target, f); !strings.Contains(why, want) {
			t.Errorf("%s: got %q, want it to mention %q", name, why, want)
		}
	}
	check("self", func(x *moderationTarget) { x.TargetID = "m" }, "yourself")
	check("bot", func(x *moderationTarget) { x.TargetID = "bot" }, "myself")
	check("owner", func(x *moderationTarget) { x.TargetID = "owner" }, "owner")
	check("equal role", func(x *moderationTarget) { x.TargetRoles = []string{"mod"} }, "above yours")
	check("above the bot", func(x *moderationTarget) { x.ModeratorRoles = []string{"admin"}; x.TargetRoles = []string{"admin"} }, "above yours")
	// The owner can act on anyone below the bot, whatever their own roles.
	ownerAct := base
	ownerAct.ModeratorID, ownerAct.ModeratorRoles = "owner", nil
	if why := moderationProblem(ownerAct, f); why != "" {
		t.Errorf("the owner should be able to act: %s", why)
	}
	ownerAct.TargetRoles = []string{"admin"}
	if why := moderationProblem(ownerAct, f); !strings.Contains(why, "above mine") {
		t.Errorf("a target above the bot should be refused: %q", why)
	}
}

func TestPurgeFilter(t *testing.T) {
	now := time.Now()
	msg := func(id, author string, bot bool, text string, age time.Duration) *discordgo.Message {
		return &discordgo.Message{ID: id, Author: &discordgo.User{ID: author, Bot: bot}, Content: text, Timestamp: now.Add(-age)}
	}
	msgs := []*discordgo.Message{
		msg("1", "a", false, "hello there", time.Minute),
		msg("2", "b", true, "beep HELLO", time.Hour),
		msg("3", "a", false, "old hello", 15*24*time.Hour),
		msg("4", "c", false, "bye", time.Minute),
		{ID: "5"}, // no author (system message)
	}
	ids, old := purgeFilter{Now: now}.pick(msgs)
	if strings.Join(ids, ",") != "1,2,4" || old != 1 {
		t.Errorf("everything: %v, %d too old", ids, old)
	}
	ids, _ = purgeFilter{Now: now, UserID: "a"}.pick(msgs)
	if strings.Join(ids, ",") != "1" {
		t.Errorf("one member: %v", ids)
	}
	ids, _ = purgeFilter{Now: now, BotsOnly: true}.pick(msgs)
	if strings.Join(ids, ",") != "2" {
		t.Errorf("bots: %v", ids)
	}
	ids, _ = purgeFilter{Now: now, Containing: "Hello"}.pick(msgs)
	if strings.Join(ids, ",") != "1,2" {
		t.Errorf("containing: %v", ids)
	}
}

func TestRenderGreeting(t *testing.T) {
	u := &discordgo.User{ID: "42", Username: "ann", GlobalName: "Ann"}
	got := renderGreeting(defaultWelcomeMessage, u, "Blazium", 120)
	if got != "Welcome to **Blazium**, <@42>! You're member #120." {
		t.Errorf("welcome: %q", got)
	}
	if got := renderGreeting("{username} left {server} ({count})", u, "Blazium", 0); got != "Ann left Blazium (?)" {
		t.Errorf("goodbye: %q", got)
	}
}

func TestMessageCache(t *testing.T) {
	c := newMessageCache(3)
	for n := 1; n <= 4; n++ {
		c.put(&cachedMessage{ID: fmt.Sprint(n), Content: fmt.Sprint("m", n)})
	}
	if c.get("1") != nil {
		t.Error("the oldest message should have been dropped")
	}
	if m := c.get("4"); m == nil || m.Content != "m4" {
		t.Errorf("newest: %+v", m)
	}
	c.put(&cachedMessage{ID: "4", Content: "edited"})
	if m := c.get("4"); m.Content != "edited" {
		t.Errorf("update: %+v", m)
	}
	if m := c.take("3"); m == nil || c.get("3") != nil {
		t.Error("take should return and forget the message")
	}
	c.put(&cachedMessage{ID: "5"})
	c.put(&cachedMessage{ID: "6"})
	if c.get("4") == nil && c.get("5") == nil {
		t.Error("cache lost too much")
	}
	if len(c.byID) > 3 {
		t.Errorf("cache grew to %d", len(c.byID))
	}
}

func TestBotIntents(t *testing.T) {
	t.Setenv("DISCORD_MEMBERS_INTENT", "")
	t.Setenv("DISCORD_MESSAGE_CONTENT_INTENT", "")
	if botIntents()&(discordgo.IntentsGuildMembers|discordgo.IntentMessageContent) != 0 {
		t.Error("privileged intents must be off unless the host turns them on")
	}
	t.Setenv("DISCORD_MEMBERS_INTENT", "true")
	t.Setenv("DISCORD_MESSAGE_CONTENT_INTENT", "1")
	if botIntents()&discordgo.IntentsGuildMembers == 0 || botIntents()&discordgo.IntentMessageContent == 0 {
		t.Error("intents should follow the environment")
	}
}

func TestHelpEntries(t *testing.T) {
	staff := permModerate
	commands := map[string]*SlashCommandHandler{
		"warn":     {Name: "warn", Description: "Warn", DefaultPermissions: &staff},
		"avatar":   {Name: "avatar", Description: "Avatar"},
		"settings": settingsCommand(),
	}
	all := helpEntries(commands, func(*SlashCommandHandler) bool { return true })
	if len(all) != 3 || all[0].Name != "avatar" || all[0].Staff || !all[2].Staff {
		t.Fatalf("entries: %+v", all)
	}
	subs := strings.Join(all[1].Subcommands, ",")
	if !strings.Contains(subs, "autorole add") || !strings.Contains(subs, "show") {
		t.Errorf("subcommands: %s", subs)
	}
	none := helpEntries(commands, func(c *SlashCommandHandler) bool { return c.DefaultPermissions == nil })
	if len(none) != 1 {
		t.Errorf("filter: %+v", none)
	}
	lines := []string{}
	for n := 0; n < 40; n++ {
		lines = append(lines, strings.Repeat("x", 60)+"\n")
	}
	fields := helpFields("Title", lines)
	if len(fields) < 2 {
		t.Fatal("long help should be split")
	}
	for _, f := range fields {
		if len(f.Value) > 1024 {
			t.Errorf("field too long: %d", len(f.Value))
		}
	}
}

func TestCommandDefinitionsAreValid(t *testing.T) {
	m := NewSlashCommandManager(&DefaultLogger{})
	defs := append(moderationCommands(), settingsCommand())
	defs = append(defs, utilityCommands()...)
	for _, c := range defs {
		c.Permissions = []string{"user"}
		if err := m.RegisterCommand(c.Name, c); err != nil {
			t.Errorf("/%s: %v", c.Name, err)
		}
		if len(c.Description) > 100 {
			t.Errorf("/%s: description too long", c.Name)
		}
		checkOptions(t, "/"+c.Name, c.Options)
	}
}

func checkOptions(t *testing.T, path string, opts []*discordgo.ApplicationCommandOption) {
	t.Helper()
	if len(opts) > 25 {
		t.Errorf("%s: %d options (Discord allows 25)", path, len(opts))
	}
	seenOptional := false
	for _, o := range opts {
		if len(o.Description) > 100 || len(o.Description) == 0 {
			t.Errorf("%s %s: description length %d", path, o.Name, len(o.Description))
		}
		if o.Type == discordgo.ApplicationCommandOptionSubCommand || o.Type == discordgo.ApplicationCommandOptionSubCommandGroup {
			checkOptions(t, path+" "+o.Name, o.Options)
			continue
		}
		if o.Required && seenOptional {
			t.Errorf("%s %s: required options must come first", path, o.Name)
		}
		if !o.Required {
			seenOptional = true
		}
	}
}

func testServerStore(t *testing.T, store serverStore) {
	g, err := store.Settings("g1")
	if err != nil || g.GuildID != "g1" || g.ModLogChannel != "" {
		t.Fatalf("empty settings: %+v, %v", g, err)
	}
	g.ModLogChannel, g.WelcomeChannel, g.WelcomeMessage, g.AutoRoles = "c1", "c2", "hi {user}", []string{"r1", "r2"}
	if err := store.SaveSettings(g); err != nil {
		t.Fatal(err)
	}
	g.AutoRoles = append(g.AutoRoles, "mutated")
	got, err := store.Settings("g1")
	if err != nil || got.ModLogChannel != "c1" || got.WelcomeMessage != "hi {user}" || strings.Join(got.AutoRoles, ",") != "r1,r2" {
		t.Fatalf("saved settings: %+v, %v", got, err)
	}
	got.AutoRoles = nil
	if err := store.SaveSettings(got); err != nil {
		t.Fatal(err)
	}
	if again, _ := store.Settings("g1"); len(again.AutoRoles) != 0 {
		t.Errorf("auto-roles should be cleared: %v", again.AutoRoles)
	}

	for n, a := range []string{caseWarn, caseTimeout, caseBan} {
		number, err := store.AddCase(&modCase{GuildID: "g1", Action: a, UserID: "u1", ModeratorID: "m", Reason: "r", Duration: time.Duration(n) * time.Hour})
		if err != nil || number != int64(n+1) {
			t.Fatalf("case %d: %d, %v", n, number, err)
		}
	}
	if number, _ := store.AddCase(&modCase{GuildID: "g2", Action: caseWarn, UserID: "u1", ModeratorID: "m"}); number != 1 {
		t.Errorf("each server counts its own cases: %d", number)
	}
	store.AddCase(&modCase{GuildID: "g1", Action: caseKick, UserID: "u2", ModeratorID: "m"})
	list, err := store.Cases("g1", "u1", 0)
	if err != nil || len(list) != 3 || list[0].Number != 3 || list[1].Duration != time.Hour {
		t.Fatalf("cases of u1: %+v, %v", list, err)
	}
	if all, _ := store.Cases("g1", "", 2); len(all) != 2 || all[0].Number != 4 {
		t.Errorf("latest cases: %+v", all)
	}
	if ok, err := store.UpdateCaseReason("g1", 2, "spam"); !ok || err != nil {
		t.Errorf("update reason: %v %v", ok, err)
	}
	if c, _ := store.Case("g1", 2); c == nil || c.Reason != "spam" || c.Action != caseTimeout {
		t.Errorf("case 2: %+v", c)
	}
	if ok, _ := store.UpdateCaseReason("g2", 2, "x"); ok {
		t.Error("another server's case must not change")
	}
	if ok, err := store.DeleteCase("g1", 1); !ok || err != nil {
		t.Errorf("delete: %v %v", ok, err)
	}
	if c, _ := store.Case("g1", 1); c != nil {
		t.Error("case 1 should be gone")
	}
	if ok, _ := store.DeleteCase("g1", 1); ok {
		t.Error("deleting twice should report false")
	}
}

func TestMemServerStore(t *testing.T) {
	testServerStore(t, newMemServerStore())
}

// Runs against a real database only when POSTGRES_CON_STRING is set (CI provides one).
func TestPostgresServerStore(t *testing.T) {
	conn := strings.TrimSpace(os.Getenv("POSTGRES_CON_STRING"))
	if conn == "" {
		t.Skip("POSTGRES_CON_STRING not set")
	}
	db, err := openPostgres(conn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := newPostgresServerStore(db)
	if err != nil {
		t.Fatal(err)
	}
	cleanup := func(db *sql.DB) {
		db.Exec(`DELETE FROM ` + tableGuildSettings + ` WHERE guild_id IN ('g1', 'g2')`)
		db.Exec(`DELETE FROM ` + tableModCases + ` WHERE guild_id IN ('g1', 'g2')`)
	}
	cleanup(db)
	defer cleanup(db)
	testServerStore(t, store)
}

func TestSettingsCache(t *testing.T) {
	old := servers
	defer func() { servers = old }()
	servers = newMemServerStore()
	g := &guildSettings{GuildID: "cache-test", MessageLogChannel: "log"}
	if err := saveServerSettings(g); err != nil {
		t.Fatal(err)
	}
	if messageLogChannel("cache-test", "general") != "log" {
		t.Error("the message log should apply to other channels")
	}
	if messageLogChannel("cache-test", "log") != "" {
		t.Error("changes in the log channel itself must not be logged")
	}
	if !strings.Contains(describeSettings(serverSettings("cache-test")), "Message log: <#log>") {
		t.Error("describeSettings should show the message log")
	}
}

func TestChannelPermissions(t *testing.T) {
	send, manage := int64(discordgo.PermissionSendMessages), int64(discordgo.PermissionManageChannels)
	roles := map[string]*discordgo.Role{
		"g":     {ID: "g", Permissions: send},
		"mod":   {ID: "mod", Permissions: send},
		"admin": {ID: "admin", Permissions: discordgo.PermissionAdministrator},
	}
	ch := &discordgo.Channel{PermissionOverwrites: []*discordgo.PermissionOverwrite{
		{ID: "g", Type: discordgo.PermissionOverwriteTypeRole, Deny: send},
		{ID: "mod", Type: discordgo.PermissionOverwriteTypeRole, Allow: manage},
		{ID: "banned", Type: discordgo.PermissionOverwriteTypeMember, Deny: manage},
	}}
	if p := channelPermissions("g", "owner", "u", nil, roles, ch); p&(send|manage) != 0 {
		t.Errorf("a plain member: %b", p)
	}
	if p := channelPermissions("g", "owner", "u", []string{"mod"}, roles, ch); p&manage == 0 || p&send != 0 {
		t.Errorf("a mod gets Manage Channels from the overwrite but not Send: %b", p)
	}
	if p := channelPermissions("g", "owner", "banned", []string{"mod"}, roles, ch); p&manage != 0 {
		t.Errorf("a member overwrite beats role overwrites: %b", p)
	}
	if p := channelPermissions("g", "owner", "u", []string{"admin"}, roles, ch); p != discordgo.PermissionAll {
		t.Error("Administrator has every permission")
	}
	if p := channelPermissions("g", "owner", "owner", nil, roles, ch); p != discordgo.PermissionAll {
		t.Error("the owner has every permission")
	}
}

// Every command that changes the server or acts on members is hidden from regular members
// by default (Discord's default permissions) and checks again when used.
func TestStaffCommandsHiddenByDefault(t *testing.T) {
	everyone := map[string]bool{"serverinfo": true, "userinfo": true, "avatar": true, "roleinfo": true, "remind": true}
	defs := append(moderationCommands(), settingsCommand(), announceCommand(), remindCommand())
	defs = append(defs, utilityCommands()...)
	for _, c := range defs {
		if everyone[c.Name] != (c.DefaultPermissions == nil) {
			t.Errorf("/%s: default permissions %v", c.Name, c.DefaultPermissions)
		}
	}
}
