package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
)

func TestParseMessageLink(t *testing.T) {
	g, c, m, ok := parseMessageLink("https://discord.com/channels/111/222/333")
	if !ok || g != "111" || c != "222" || m != "333" {
		t.Errorf("got %s %s %s %v", g, c, m, ok)
	}
	if _, _, _, ok := parseMessageLink("https://ptb.discordapp.com/channels/1/2/3"); !ok {
		t.Error("ptb links should work")
	}
	if _, _, _, ok := parseMessageLink("https://example.com/1/2/3"); ok {
		t.Error("other links must be refused")
	}
}

func TestParseColor(t *testing.T) {
	good := map[string]int{"": 0x5865F2, "#ff8800": 0xFF8800, "FF8800": 0xFF8800, "0xff8800": 0xFF8800, "Red": 0xED4245}
	for in, want := range good {
		if got, err := parseColor(in); err != nil || got != want {
			t.Errorf("parseColor(%q) = %X, %v", in, got, err)
		}
	}
	for _, bad := range []string{"#fff", "zzzzzz", "rainbow"} {
		if _, err := parseColor(bad); err == nil {
			t.Errorf("parseColor(%q) should fail", bad)
		}
	}
}

func TestReadAnnouncement(t *testing.T) {
	fields := func(kv ...string) map[string]modalValue {
		out := map[string]modalValue{}
		for n := 0; n < len(kv); n += 2 {
			out[kv[n]] = modalValue{values: []string{kv[n+1]}}
		}
		return out
	}
	a, err := readAnnouncement(fields("title", " Hello ", "body", "World", "color", "blue", "image", "https://x.test/a.png", "footer", "f"))
	if err != nil || a.Title != "Hello" || a.Color != 0x3498DB || a.Image == "" {
		t.Fatalf("%+v %v", a, err)
	}
	e := a.embed()
	if e.Image == nil || e.Footer == nil || e.Footer.Text != "f" {
		t.Errorf("embed: %+v", e)
	}
	if _, err := readAnnouncement(fields("title", "", "body", " ")); err == nil {
		t.Error("an empty announcement must be refused")
	}
	if _, err := readAnnouncement(fields("title", "x", "image", "javascript:alert(1)")); err == nil {
		t.Error("a bad image link must be refused")
	}
}

func TestPingContent(t *testing.T) {
	if text, m := pingContent(""); text != "" || len(m.Parse)+len(m.Roles) != 0 {
		t.Error("no ping should notify nobody")
	}
	if text, m := pingContent("here"); text != "@here" || len(m.Parse) != 1 {
		t.Errorf("here: %q %+v", text, m)
	}
	if text, m := pingContent("42"); text != "<@&42>" || len(m.Roles) != 1 || len(m.Parse) != 0 {
		t.Errorf("role: %q %+v", text, m)
	}
}

func TestAnnouncementModal(t *testing.T) {
	data := announcementModal("ann:1::0", "New announcement", announcement{Title: "t", Color: 0xFF8800})
	if len(data.Components) != 5 || len(data.Title) > 45 || len(data.CustomID) > 100 {
		t.Fatalf("modal: %+v", data)
	}
	label := data.Components[2].(discordgo.Label)
	if label.Component.(discordgo.TextInput).Value != "#FF8800" {
		t.Error("the color should be filled in when editing")
	}
}

func TestPlanReminder(t *testing.T) {
	now := time.Now()
	due, text, err := planReminder("2h", "  check the build ", now, 0)
	if err != nil || !due.Equal(now.Add(2*time.Hour)) || text != "check the build" {
		t.Errorf("%v %q %v", due, text, err)
	}
	for _, c := range []struct {
		in, text string
		owned    int
	}{
		{"30s", "x", 0}, {"2y", "x", 0}, {"400d", "x", 0}, {"1h", " ", 0}, {"1h", "x", maxRemindersOwned},
	} {
		if _, _, err := planReminder(c.in, c.text, now, c.owned); err == nil {
			t.Errorf("planReminder(%q, %q, %d) should fail", c.in, c.text, c.owned)
		}
	}
}

func testRemindersStore(t *testing.T, store remindersStore) {
	now := time.Now().Truncate(time.Second)
	add := func(user, guild string, in time.Duration) int64 {
		id, err := store.Add(&reminder{UserID: user, GuildID: guild, ChannelID: "c", Text: "t", DueAt: now.Add(in)})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	late := add("rem-u1", "rem-g1", 2*time.Hour)
	soon := add("rem-u1", "rem-g1", time.Hour)
	add("rem-u1", "rem-g2", time.Hour)
	other := add("rem-u2", "rem-g1", -time.Minute)
	mine, err := store.Of("rem-g1", "rem-u1")
	if err != nil || len(mine) != 2 || mine[0].ID != soon || mine[1].ID != late {
		t.Fatalf("Of: %+v %v", mine, err)
	}
	if ok, _ := store.Cancel("rem-u2", late); ok {
		t.Error("nobody may cancel someone else's reminder")
	}
	if ok, err := store.Cancel("rem-u1", late); !ok || err != nil {
		t.Errorf("cancel: %v %v", ok, err)
	}
	due, err := store.TakeDue(now, 10)
	if err != nil || len(due) != 1 || due[0].ID != other {
		t.Fatalf("due: %+v %v", due, err)
	}
	if again, _ := store.TakeDue(now, 10); len(again) != 0 {
		t.Error("a taken reminder must not be taken twice")
	}
	rest, _ := store.TakeDue(now.Add(3*time.Hour), 10)
	if len(rest) != 2 {
		t.Errorf("rest: %+v", rest)
	}
}

func TestMemRemindersStore(t *testing.T) { testRemindersStore(t, newMemRemindersStore()) }

func TestPostgresRemindersStore(t *testing.T) {
	conn := strings.TrimSpace(os.Getenv("POSTGRES_CON_STRING"))
	if conn == "" {
		t.Skip("POSTGRES_CON_STRING not set")
	}
	db, err := openPostgres(conn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := newPostgresRemindersStore(db)
	if err != nil {
		t.Fatal(err)
	}
	cleanup := func() { db.Exec(`DELETE FROM ` + tableReminders + ` WHERE user_id LIKE 'rem-%'`) }
	cleanup()
	defer cleanup()
	testRemindersStore(t, store)
}

func TestNewCommandDefinitions(t *testing.T) {
	m := NewSlashCommandManager(&DefaultLogger{})
	for _, c := range []*SlashCommandHandler{announceCommand(), remindCommand()} {
		c.Permissions = []string{"user"}
		if err := m.RegisterCommand(c.Name, c); err != nil {
			t.Errorf("/%s: %v", c.Name, err)
		}
		checkOptions(t, "/"+c.Name, c.Options)
	}
}
