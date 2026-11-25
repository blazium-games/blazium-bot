package main

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
)

func testRoleFacts() *guildRoleFacts {
	roles := map[string]*discordgo.Role{
		"100": {ID: "100", Name: "@everyone", Position: 0},
		"1":   {ID: "1", Name: "Player", Position: 1},
		"2":   {ID: "2", Name: "Spectator", Position: 2},
		"3":   {ID: "3", Name: "Mod", Position: 3, Permissions: discordgo.PermissionKickMembers | discordgo.PermissionBanMembers},
		"4":   {ID: "4", Name: "Booster", Position: 4, Managed: true},
		"5":   {ID: "5", Name: "Bot", Position: 5, Permissions: discordgo.PermissionManageRoles},
		"6":   {ID: "6", Name: "Above bot", Position: 6},
	}
	return &guildRoleFacts{GuildID: "100", Roles: roles, BotTop: 5, BotManageRoles: true}
}

func TestRoleProblem(t *testing.T) {
	f := testRoleFacts()
	cases := map[string]string{
		"1":   "",
		"100": "@everyone",
		"3":   "staff-level permissions (Kick Members, Ban Members)",
		"4":   "managed",
		"5":   "staff-level", // Manage Roles is risky before the position check
		"6":   "above",
	}
	for id, want := range cases {
		got := roleProblem(f.Roles[id], f)
		if want == "" && got != "" {
			t.Errorf("role %s: want no problem, got %q", id, got)
		}
		if want != "" && !strings.Contains(got, want) {
			t.Errorf("role %s: want %q in %q", id, want, got)
		}
	}
	f.BotManageRoles = false
	if got := roleProblem(f.Roles["1"], f); !strings.Contains(got, "Manage Roles") {
		t.Errorf("without Manage Roles: got %q", got)
	}
}

func TestGuildPermissionsAndTop(t *testing.T) {
	f := testRoleFacts()
	f.Roles["100"].Permissions = discordgo.PermissionViewChannel
	perms := guildPermissions("100", []string{"1", "5"}, f.Roles)
	if perms&discordgo.PermissionManageRoles == 0 || perms&discordgo.PermissionViewChannel == 0 {
		t.Fatalf("perms = %b", perms)
	}
	if top := topPosition([]string{"1", "3", "missing"}, f.Roles); top != 3 {
		t.Fatalf("top = %d", top)
	}
}

func noProblem(string) string { return "" }

func TestPlanSignupClick(t *testing.T) {
	panelRoles := []string{"1", "2"}

	c, err := planSignupClick(signupModeToggle, nil, "1", panelRoles, noProblem)
	if err != nil || c.Add != "1" || len(c.Remove) != 0 {
		t.Fatalf("toggle on: %+v %v", c, err)
	}
	c, _ = planSignupClick(signupModeToggle, []string{"1"}, "1", panelRoles, noProblem)
	if c.Add != "" || len(c.Remove) != 1 || c.Remove[0] != "1" {
		t.Fatalf("toggle off: %+v", c)
	}

	c, _ = planSignupClick(signupModeAddOnly, []string{"1"}, "1", panelRoles, noProblem)
	if c.Add != "" || len(c.Remove) != 0 || !strings.Contains(c.Message, "already") {
		t.Fatalf("add_only held: %+v", c)
	}
	c, _ = planSignupClick(signupModeAddOnly, nil, "1", panelRoles, noProblem)
	if c.Add != "1" {
		t.Fatalf("add_only new: %+v", c)
	}

	c, _ = planSignupClick(signupModeSingle, []string{"2", "999"}, "1", panelRoles, noProblem)
	if c.Add != "1" || len(c.Remove) != 1 || c.Remove[0] != "2" || !strings.Contains(c.Message, "replaced") {
		t.Fatalf("single swap: %+v", c)
	}
	c, _ = planSignupClick(signupModeSingle, []string{"1"}, "1", panelRoles, noProblem)
	if c.Add != "" || len(c.Remove) != 1 {
		t.Fatalf("single off: %+v", c)
	}

	blocked := func(id string) string {
		if id == "2" {
			return "it carries staff-level permissions"
		}
		return ""
	}
	_, err = planSignupClick(signupModeSingle, []string{"2"}, "1", panelRoles, blocked)
	if err == nil || !strings.Contains(err.Error(), "can't swap") {
		t.Fatalf("single blocked: %v", err)
	}
}

func TestSignupButtonID(t *testing.T) {
	id := signupButtonID(12, "123456789012345678")
	if id != "signup:12:123456789012345678" {
		t.Fatalf("id = %q", id)
	}
	panel, role, ok := parseSignupButtonID(id)
	if !ok || panel != 12 || role != "123456789012345678" {
		t.Fatalf("parse = %d %q %v", panel, role, ok)
	}
	for _, bad := range []string{"signup:x:1", "signup:1", "signup:0:1", "form:1:2", "signup:1:abc"} {
		if _, _, ok := parseSignupButtonID(bad); ok {
			t.Errorf("%q parsed", bad)
		}
	}
	if routePrefix(id) != "signup" {
		t.Fatal("prefix")
	}
}

func TestParseButtonEmoji(t *testing.T) {
	e, err := parseButtonEmoji("🎮")
	if err != nil || e == nil || e.Name != "🎮" {
		t.Fatalf("unicode: %+v %v", e, err)
	}
	e, err = parseButtonEmoji("<a:blazium:123456789012345678>")
	if err != nil || e.ID != "123456789012345678" || e.Name != "blazium" || !e.Animated {
		t.Fatalf("custom: %+v %v", e, err)
	}
	if e, err := parseButtonEmoji("none"); e != nil || err != nil {
		t.Fatal("none")
	}
	for _, bad := range []string{"hello", "a", ":smile:"} {
		if _, err := parseButtonEmoji(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestBuildSignupPanel(t *testing.T) {
	p := &signupPanel{ID: 7, Title: "Season", Description: "Join!", Mode: signupModeSingle}
	var roles []signupPanelRole
	for n := 0; n < 7; n++ {
		roles = append(roles, signupPanelRole{PanelID: 7, RoleID: string(rune('1' + n)), Label: "R", Style: int(discordgo.SuccessButton)})
	}
	roles[0].Emoji = "🎮"
	embed, rows := buildSignupPanel(p, roles)
	if len(rows) != 2 {
		t.Fatalf("rows = %d", len(rows))
	}
	first := rows[0].(discordgo.ActionsRow)
	if len(first.Components) != 5 || len(rows[1].(discordgo.ActionsRow).Components) != 2 {
		t.Fatal("5 buttons per row")
	}
	b := first.Components[0].(discordgo.Button)
	if b.CustomID != "signup:7:1" || b.Emoji == nil || b.Style != discordgo.SuccessButton {
		t.Fatalf("button = %+v", b)
	}
	if !strings.Contains(embed.Description, "Join!") || embed.Footer.Text != signupModeHelp[signupModeSingle] {
		t.Fatalf("embed = %+v", embed)
	}
	embed, rows = buildSignupPanel(&signupPanel{ID: 1, Title: "Empty"}, nil)
	if len(rows) != 0 || !strings.Contains(embed.Description, "No sign-up options") {
		t.Fatal("empty panel")
	}
}

func exerciseSignupStore(t *testing.T, store signupStore, guild string) {
	t.Helper()
	id, err := store.CreatePanel(&signupPanel{GuildID: guild, ChannelID: "c1", Title: "T", Mode: signupModeToggle, CreatedBy: "u"})
	if err != nil {
		t.Fatal(err)
	}
	if p, _ := store.GetPanel("other-guild", id); p != nil {
		t.Fatal("panel visible from another server")
	}
	p, err := store.GetPanel(guild, id)
	if err != nil || p == nil || p.Title != "T" {
		t.Fatalf("get: %+v %v", p, err)
	}
	p.MessageID, p.Mode = "m1", signupModeSingle
	if err := store.UpdatePanel(p); err != nil {
		t.Fatal(err)
	}
	for n, role := range []string{"r1", "r2"} {
		if err := store.AddPanelRole(&signupPanelRole{PanelID: id, RoleID: role, Label: role, Style: 2, Position: n}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.AddPanelRole(&signupPanelRole{PanelID: id, RoleID: "r1", Label: "dup"}); err != errAlreadyExists {
		t.Fatalf("duplicate role: %v", err)
	}
	list, _ := store.ListPanels(guild)
	if len(list) != 1 || list[0].RoleCount != 2 || list[0].MessageID != "m1" || list[0].Mode != signupModeSingle {
		t.Fatalf("list = %+v", list)
	}
	touched, err := store.RemoveRoleEverywhere(guild, "r1")
	if err != nil || len(touched) != 1 || touched[0] != id {
		t.Fatalf("remove everywhere: %v %v", touched, err)
	}
	if err := store.RemovePanelRole(id, "r2"); err != nil {
		t.Fatal(err)
	}
	if roles, _ := store.PanelRoles(id); len(roles) != 0 {
		t.Fatalf("roles left: %+v", roles)
	}
	if err := store.DeletePanel(id); err != nil {
		t.Fatal(err)
	}
	if p, _ := store.GetPanel(guild, id); p != nil {
		t.Fatal("panel not deleted")
	}
}

func TestMemSignupStore(t *testing.T) {
	exerciseSignupStore(t, newMemSignupStore(), "g1")
}

// Runs against a real database only when POSTGRES_CON_STRING is set (CI provides one).
func TestPostgresSignupStore(t *testing.T) {
	conn := strings.TrimSpace(os.Getenv("POSTGRES_CON_STRING"))
	if conn == "" {
		t.Skip("POSTGRES_CON_STRING not set")
	}
	db, err := openPostgres(conn)
	if err != nil {
		t.Skipf("Postgres unavailable: %v", err)
	}
	defer db.Close()
	store, err := newPostgresSignupStore(db)
	if err != nil {
		t.Fatal(err)
	}
	guild := "test-" + newMarketingID()
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DELETE FROM `+tableSignupPanels+` WHERE guild_id = $1`, guild)
	})
	exerciseSignupStore(t, store, guild)
}

func TestPanelCommandDefaults(t *testing.T) {
	m := NewSlashCommandManager(&LoggerAdapter{})
	CommandManager = m
	t.Cleanup(func() { CommandManager = nil })
	registerSignupPanels()
	var panel *discordgo.ApplicationCommand
	for _, c := range m.BuildCommandSlice() {
		if c.Name == "panel" {
			panel = c
		}
	}
	if panel == nil {
		t.Fatal("/panel not registered")
	}
	if panel.DefaultMemberPermissions == nil || *panel.DefaultMemberPermissions != discordgo.PermissionManageGuild {
		t.Fatalf("default permissions = %v", panel.DefaultMemberPermissions)
	}
	if panel.DMPermission == nil || *panel.DMPermission {
		t.Fatal("/panel should be server-only")
	}
	if len(panel.Options) != 8 {
		t.Fatalf("subcommands = %d", len(panel.Options))
	}
}

func TestCanManageServer(t *testing.T) {
	i := &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{Member: &discordgo.Member{Permissions: discordgo.PermissionManageGuild}}}
	if !canManageServer(i) {
		t.Fatal("Manage Server")
	}
	i.Member.Permissions = 0
	i.Member.Roles = []string{"staff"}
	t.Setenv("STAFF_ROLE_ID", "staff")
	if !canManageServer(i) {
		t.Fatal("staff role")
	}
	t.Setenv("STAFF_ROLE_ID", "")
	if canManageServer(i) {
		t.Fatal("no permission")
	}
}
