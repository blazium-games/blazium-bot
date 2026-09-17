package main

import (
	"reflect"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
)

func TestCollectGuildIDs(t *testing.T) {
	guilds := []*discordgo.Guild{
		{ID: "111", Name: "Blazium Games"},
		{ID: "", Name: "empty"},
		nil,
		{ID: "222", Name: "BlaziumCore"},
		{ID: "333", Name: "down", Unavailable: true},
		{ID: "111", Name: "duplicate"},
	}
	got := collectGuildIDs(guilds, " 1294004245489782896 ")
	want := []string{"111", "222", "1294004245489782896"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestCollectGuildIDsExtraAlreadyPresent(t *testing.T) {
	guilds := []*discordgo.Guild{{ID: "1294004245489782896", Name: "Blazium Games"}}
	got := collectGuildIDs(guilds, "1294004245489782896")
	want := []string{"1294004245489782896"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestCollectGuildIDsNone(t *testing.T) {
	got := collectGuildIDs(nil, "")
	if len(got) != 0 {
		t.Fatalf("expected empty, got %v", got)
	}
}

func TestGuildCommandResyncDelays(t *testing.T) {
	want := []time.Duration{30 * time.Second, 90 * time.Second}
	if !reflect.DeepEqual(guildCommandResyncDelays, want) {
		t.Fatalf("got %v want %v", guildCommandResyncDelays, want)
	}
}

func TestCommandRequiresAdmin(t *testing.T) {
	if commandRequiresAdmin(&SlashCommandHandler{Permissions: []string{"user"}}) {
		t.Fatal("user should not require admin")
	}
	if !commandRequiresAdmin(&SlashCommandHandler{Permissions: []string{"admin"}}) {
		t.Fatal("admin should require admin")
	}
}

func TestBuildCommandSliceAdminPermissions(t *testing.T) {
	m := NewSlashCommandManager(&DefaultLogger{})
	if err := m.RegisterCommand("secret", &SlashCommandHandler{
		Name:        "secret",
		Description: "Admin only",
		Handler:     func(s *discordgo.Session, i *discordgo.InteractionCreate) {},
		Permissions: []string{"admin"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := m.RegisterCommand("marketing", &SlashCommandHandler{
		Name:        "marketing",
		Description: "Staff marketing",
		Handler:     func(s *discordgo.Session, i *discordgo.InteractionCreate) {},
		Permissions: []string{"user"},
	}); err != nil {
		t.Fatal(err)
	}
	cmds := m.BuildCommandSlice()
	var secret, marketing *discordgo.ApplicationCommand
	for _, c := range cmds {
		switch c.Name {
		case "secret":
			secret = c
		case "marketing":
			marketing = c
		}
	}
	if secret == nil || secret.DefaultMemberPermissions == nil || *secret.DefaultMemberPermissions != int64(discordgo.PermissionAdministrator) {
		t.Fatalf("admin commands should default to administrator: %+v", secret)
	}
	if marketing == nil || marketing.DefaultMemberPermissions != nil {
		t.Fatalf("marketing must stay visible to staff without administrator: %+v", marketing)
	}
	if marketing.Type != discordgo.ChatApplicationCommand {
		t.Fatalf("marketing type want ChatInput got %d", marketing.Type)
	}
	if secret.Type != discordgo.ChatApplicationCommand {
		t.Fatalf("secret type want ChatInput got %d", secret.Type)
	}
}

func TestVerifyBulkOverwrite(t *testing.T) {
	if err := verifyBulkOverwrite(2, nil); err == nil {
		t.Fatal("empty overwrite should fail")
	}
	if err := verifyBulkOverwrite(2, []*discordgo.ApplicationCommand{}); err == nil {
		t.Fatal("empty overwrite slice should fail")
	}
	created := []*discordgo.ApplicationCommand{
		{Name: "help", ID: "1"},
		{Name: "test", ID: "2"},
	}
	if err := verifyBulkOverwrite(2, created); err != nil {
		t.Fatalf("matching overwrite should succeed: %v", err)
	}
	if err := verifyBulkOverwrite(3, created); err == nil {
		t.Fatal("count mismatch should fail")
	}
}

func TestHasStaffAccessIncludesAdminAndStaffRole(t *testing.T) {
	admin := &discordgo.InteractionCreate{
		Interaction: &discordgo.Interaction{
			Member: &discordgo.Member{
				User:        &discordgo.User{ID: "1", Username: "admin"},
				Permissions: discordgo.PermissionAdministrator,
			},
		},
	}
	if !hasStaffAccess(nil, admin) {
		t.Fatal("administrators should have staff access")
	}

	t.Setenv("STAFF_ROLE_ID", "role-staff")
	staff := &discordgo.InteractionCreate{
		Interaction: &discordgo.Interaction{
			Member: &discordgo.Member{
				User:  &discordgo.User{ID: "2", Username: "staff"},
				Roles: []string{"role-staff"},
			},
		},
	}
	if !hasStaffAccess(nil, staff) {
		t.Fatal("staff role should have staff access")
	}

	user := &discordgo.InteractionCreate{
		Interaction: &discordgo.Interaction{
			Member: &discordgo.Member{
				User: &discordgo.User{ID: "3", Username: "user"},
			},
		},
	}
	if hasStaffAccess(nil, user) {
		t.Fatal("regular users should not have staff access")
	}
}
