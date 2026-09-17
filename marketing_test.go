package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/gorilla/mux"
)

func TestMain(m *testing.M) {
	os.Setenv("LOG_OUTPUT", "stream")
	os.Setenv("LOG_LEVEL", "error")
	code := m.Run()
	os.Exit(code)
}

func setupMarketingTest(t *testing.T) (*httptest.Server, *memMarketingStore, *[]*discordgo.MessageEmbed) {
	t.Helper()
	store := newMemMarketingStore()
	marketingStore = store
	marketingCfg.token = strings.Repeat("a", 128)
	marketingCfg.channelID = defaultMarketingChannelID
	marketingCfg.guildID = defaultMarketingGuildID
	embeds := make([]*discordgo.MessageEmbed, 0)
	onProspectCreated = func(p *MarketingProspect) {
		embeds = append(embeds, buildNewProspectEmbed(p))
	}
	onOutreachRecorded = func(p *MarketingProspect, o *MarketingOutreach) {
		embeds = append(embeds, buildOutreachEmbed(p, o))
	}
	t.Cleanup(func() {
		marketingStore = nil
		marketingCfg.token = ""
		onProspectCreated = defaultOnProspectCreated
		onOutreachRecorded = defaultOnOutreachRecorded
	})
	r := mux.NewRouter()
	registerMarketingRoutes(r)
	ts := httptest.NewServer(r)
	t.Cleanup(ts.Close)
	return ts, store, &embeds
}

func marketingDo(t *testing.T, ts *httptest.Server, method, path, token, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func decodeBody(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestMarketingAuth(t *testing.T) {
	ts, _, _ := setupMarketingTest(t)
	token := strings.Repeat("a", 128)

	resp := marketingDo(t, ts, http.MethodGet, "/v1/marketing/exists?email=a@b.c", "", "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("missing token: got %d", resp.StatusCode)
	}
	resp.Body.Close()

	resp = marketingDo(t, ts, http.MethodGet, "/v1/marketing/exists?email=a@b.c", "short", "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong token: got %d", resp.StatusCode)
	}
	resp.Body.Close()

	resp = marketingDo(t, ts, http.MethodGet, "/v1/marketing/exists?email=a@b.c", token, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("valid token: got %d", resp.StatusCode)
	}
	body := decodeBody(t, resp)
	if body["exists"] != false {
		t.Fatalf("expected exists=false, got %#v", body["exists"])
	}
}

func TestMarketingUnavailableWithoutStore(t *testing.T) {
	marketingStore = nil
	marketingCfg.token = strings.Repeat("a", 128)
	r := mux.NewRouter()
	registerMarketingRoutes(r)
	ts := httptest.NewServer(r)
	defer ts.Close()
	resp := marketingDo(t, ts, http.MethodGet, "/v1/marketing/example", strings.Repeat("a", 128), "")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestMarketingExamplePayload(t *testing.T) {
	ts, _, _ := setupMarketingTest(t)
	resp := marketingDo(t, ts, http.MethodGet, "/v1/marketing/example", strings.Repeat("a", 128), "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("got %d", resp.StatusCode)
	}
	defer resp.Body.Close()
	var payload MarketingPayload
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if err := validateProspectInput(&payload.Prospect); err != nil {
		t.Fatal(err)
	}
	if payload.Event == nil {
		t.Fatal("expected example event")
	}
	if err := validateEventInput(payload.Event); err != nil {
		t.Fatal(err)
	}
	if payload.Prospect.Email != "dev@studio.example" {
		t.Fatalf("unexpected email %q", payload.Prospect.Email)
	}
}

func TestMarketingEnsureInsertAndDuplicate(t *testing.T) {
	ts, _, embeds := setupMarketingTest(t)
	token := strings.Repeat("a", 128)
	raw := bytes.TrimSpace(marketingExampleJSON)

	resp := marketingDo(t, ts, http.MethodPost, "/v1/marketing/prospects/ensure", token, string(raw))
	body := decodeBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ensure create: %d %#v", resp.StatusCode, body)
	}
	if body["created"] != true {
		t.Fatalf("expected created=true: %#v", body)
	}
	if len(*embeds) < 2 {
		t.Fatalf("expected new-entry and outbound embeds, got %d", len(*embeds))
	}
	if (*embeds)[0].Title != "New marketing prospect" {
		t.Fatalf("first embed %q", (*embeds)[0].Title)
	}
	if (*embeds)[1].Title != "Outreach sent" {
		t.Fatalf("second embed %q", (*embeds)[1].Title)
	}

	resp = marketingDo(t, ts, http.MethodGet, "/v1/marketing/exists?email=dev@studio.example", token, "")
	exists := decodeBody(t, resp)
	if exists["exists"] != true {
		t.Fatalf("expected exists: %#v", exists)
	}

	before := len(*embeds)
	resp = marketingDo(t, ts, http.MethodPost, "/v1/marketing/prospects/ensure", token, string(raw))
	again := decodeBody(t, resp)
	if again["created"] != false {
		t.Fatalf("expected created=false on duplicate: %#v", again)
	}
	if len(*embeds) != before+1 {
		t.Fatalf("duplicate ensure with send should add one outbound embed, before=%d after=%d", before, len(*embeds))
	}

	resp = marketingDo(t, ts, http.MethodPost, "/v1/marketing/prospects", token, string(raw))
	conflict := decodeBody(t, resp)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("insert existing: %d %#v", resp.StatusCode, conflict)
	}
}

func TestMarketingOutreachLedger(t *testing.T) {
	ts, _, _ := setupMarketingTest(t)
	token := strings.Repeat("a", 128)
	payload := `{
		"prospect": {
			"email": "dev@studio.example",
			"display_name": "Jane Dev",
			"persona": "godot_steam_unreleased",
			"engine_familiarity": "godot",
			"need": "steam",
			"project_name": "Dungeon Crawler",
			"public_url": "https://store.steampowered.com/app/1234560",
			"source": "steam",
			"legal_ok": true,
			"campaign": "godot-4-steam-unreleased"
		}
	}`
	resp := marketingDo(t, ts, http.MethodPost, "/v1/marketing/prospects", token, payload)
	created := decodeBody(t, resp)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d %#v", resp.StatusCode, created)
	}
	prospect := created["prospect"].(map[string]any)
	id := prospect["id"].(string)

	eventBody := `{
		"prospect_id": "` + id + `",
		"type": "send",
		"channel": "email",
		"campaign": "godot-4-steam-unreleased",
		"subject": "hello",
		"touch_number": 1,
		"occurred_at": "2026-09-17T21:00:00Z"
	}`
	resp = marketingDo(t, ts, http.MethodPost, "/v1/marketing/events", token, eventBody)
	ev := decodeBody(t, resp)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("event: %d %#v", resp.StatusCode, ev)
	}
	if ev["outreach"] == nil {
		t.Fatal("expected outreach row")
	}

	resp = marketingDo(t, ts, http.MethodGet, "/v1/marketing/outreach?email=dev@studio.example", token, "")
	list := decodeBody(t, resp)
	rows, _ := list["outreach"].([]any)
	if len(rows) != 1 {
		t.Fatalf("expected 1 outreach row, got %#v", list)
	}
	row := rows[0].(map[string]any)
	if row["email"] != "dev@studio.example" {
		t.Fatalf("outreach email %#v", row["email"])
	}
	if row["subject"] != "hello" {
		t.Fatalf("outreach subject %#v", row["subject"])
	}

	resp = marketingDo(t, ts, http.MethodGet, "/v1/marketing/prospects/"+id+"/outreach", token, "")
	byID := decodeBody(t, resp)
	idRows, _ := byID["outreach"].([]any)
	if len(idRows) != 1 {
		t.Fatalf("expected 1 row by prospect, got %#v", byID)
	}
}

func TestValidateProspectRequiresIdentity(t *testing.T) {
	p := MarketingProspect{Persona: "x", Source: "itch"}
	if err := validateProspectInput(&p); err == nil {
		t.Fatal("expected identity error")
	}
	p.Email = "Dev@Studio.Example"
	if err := validateProspectInput(&p); err != nil {
		t.Fatal(err)
	}
	if p.Email != "Dev@Studio.Example" {
		t.Fatalf("email should not be lowercased in stored form beyond trim; got %q", p.Email)
	}
	p.EngineFamiliarity = "godot"
	p.Need = "not-a-need"
	if err := validateProspectInput(&p); err == nil {
		t.Fatal("expected need enum error")
	}
}

func TestBuildMarketingEmbeds(t *testing.T) {
	now := time.Date(2026, 9, 17, 21, 0, 0, 0, time.UTC)
	p := &MarketingProspect{
		ID:          "abc",
		Email:       "dev@studio.example",
		DisplayName: "Jane Dev",
		ProjectName: "Dungeon Crawler",
		Persona:     "godot_steam_unreleased",
		Need:        "steam",
		Source:      "steam",
		Campaign:    "godot-4-steam-unreleased",
		PublicURL:   "https://example.com",
		Status:      "new",
		CreatedAt:   now,
	}
	newEmbed := buildNewProspectEmbed(p)
	if newEmbed.Title != "New marketing prospect" || newEmbed.Color != marketingEmbedNewColor {
		t.Fatalf("new embed %#v", newEmbed)
	}
	o := &MarketingOutreach{
		ID:          "out1",
		Email:       p.Email,
		DisplayName: p.DisplayName,
		Campaign:    p.Campaign,
		Channel:     "email",
		Subject:     "hello",
		TouchNumber: 1,
		OccurredAt:  now,
	}
	outEmbed := buildOutreachEmbed(p, o)
	if outEmbed.Title != "Outreach sent" || outEmbed.Color != marketingEmbedOutreachColor {
		t.Fatalf("outreach embed %#v", outEmbed)
	}
	foundTouch := false
	for _, f := range outEmbed.Fields {
		if f.Name == "Touch" && f.Value == "1 / 3" {
			foundTouch = true
		}
	}
	if !foundTouch {
		t.Fatalf("missing touch field: %#v", outEmbed.Fields)
	}
}

func TestXGrokbotTokenHeader(t *testing.T) {
	ts, _, _ := setupMarketingTest(t)
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/marketing/exists?email=a@b.c", nil)
	req.Header.Set("X-Grokbot-Token", strings.Repeat("a", 128))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestBotTablePrefix(t *testing.T) {
	if botTablePrefix != "bot_" {
		t.Fatalf("prefix %q", botTablePrefix)
	}
	if tableProspects != "bot_marketing_prospects" || tableEvents != "bot_marketing_events" || tableOutreach != "bot_marketing_outreach" {
		t.Fatalf("tables %s %s %s", tableProspects, tableEvents, tableOutreach)
	}
}

func TestMarketingStatsAndInfoEmbed(t *testing.T) {
	store := newMemMarketingStore()
	p := &MarketingProspect{
		Email:       "dev@studio.example",
		DisplayName: "Jane",
		Persona:     "godot_steam_unreleased",
		Need:        "steam",
		Source:      "steam",
		Campaign:    "godot-4-steam-unreleased",
		Status:      "new",
		PublicURL:   "https://example.com",
	}
	if err := validateProspectInput(p); err != nil {
		t.Fatal(err)
	}
	if err := store.InsertProspect(p); err != nil {
		t.Fatal(err)
	}
	ev := &MarketingEvent{Type: "send", Channel: "email", Subject: "hello", TouchNumber: 1, OccurredAt: time.Now().UTC()}
	if err := validateEventInput(ev); err != nil {
		t.Fatal(err)
	}
	marketingStore = store
	onProspectCreated = func(*MarketingProspect) {}
	onOutreachRecorded = func(*MarketingProspect, *MarketingOutreach) {}
	t.Cleanup(func() {
		marketingStore = nil
		onProspectCreated = defaultOnProspectCreated
		onOutreachRecorded = defaultOnOutreachRecorded
	})
	if _, _, err := recordEvent(p, ev); err != nil {
		t.Fatal(err)
	}
	st, err := store.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if st.Prospects != 1 || st.Outreach != 1 {
		t.Fatalf("stats %+v", st)
	}
	if st.ByStatus["contacted"] != 1 {
		t.Fatalf("status %+v", st.ByStatus)
	}
	if st.EventsByType["send"] != 1 {
		t.Fatalf("events %+v", st.EventsByType)
	}
	if formatCountMap(st.ByCampaign, 8) == "none" {
		t.Fatal("expected campaign counts")
	}
	got, err := store.GetProspect(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	rows, _ := store.ListOutreach(outreachFilters{ProspectID: p.ID, Limit: 5})
	embed := buildMarketingInfoEmbed(got, rows)
	if embed.Title != "Marketing entry" || !strings.Contains(formatOutreachList(rows), "hello") {
		t.Fatalf("info embed %#v list %q", embed, formatOutreachList(rows))
	}
}
