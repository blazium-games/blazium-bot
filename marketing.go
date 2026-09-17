package main

import (
	"crypto/rand"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

//go:embed marketing_example.json
var marketingExampleJSON []byte

const (
	defaultMarketingGuildID   = "1294004245489782896"
	defaultMarketingChannelID = "1540191811497103380"
	marketingTouchCap         = 3
)

var (
	errAlreadyExists = errors.New("already exists")
	errNotFound      = errors.New("not found")
)

var (
	engineFamiliarityValues = []string{"godot", "unity", "unreal", "none"}
	needValues              = []string{"lobby", "steam", "discord", "youtube", "idle", "ai", "retro", "ci"}
	statusValues            = []string{"new", "queued", "contacted", "replied", "bounced", "unsubscribed", "excluded", "reviewed"}
	eventTypeValues         = []string{"send", "reply", "bounce", "unsubscribe", "exclude", "review"}
)

type marketingRuntimeConfig struct {
	token      string
	connString string
	guildID    string
	channelID  string
}

var marketingCfg marketingRuntimeConfig

type MarketingProspect struct {
	ID                string     `json:"id"`
	Email             string     `json:"email"`
	DisplayName       string     `json:"display_name"`
	Persona           string     `json:"persona"`
	EngineFamiliarity string     `json:"engine_familiarity"`
	Need              string     `json:"need"`
	ProjectName       string     `json:"project_name"`
	PublicURL         string     `json:"public_url"`
	LastShipDate      string     `json:"last_ship_date,omitempty"`
	Tone              string     `json:"tone"`
	Source            string     `json:"source"`
	SourceURL         string     `json:"source_url"`
	LegalOK           bool       `json:"legal_ok"`
	Status            string     `json:"status"`
	Campaign          string     `json:"campaign"`
	Tags              []string   `json:"tags"`
	Notes             string     `json:"notes"`
	TouchCount        int        `json:"touch_count"`
	LastEventAt       *time.Time `json:"last_event_at,omitempty"`
	LastOutreachAt    *time.Time `json:"last_outreach_at,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

type MarketingEvent struct {
	ID          string          `json:"id"`
	ProspectID  string          `json:"prospect_id"`
	Type        string          `json:"type"`
	Channel     string          `json:"channel"`
	Campaign    string          `json:"campaign"`
	Subject     string          `json:"subject"`
	TouchNumber int             `json:"touch_number"`
	OccurredAt  time.Time       `json:"occurred_at"`
	Metadata    json.RawMessage `json:"metadata"`
	CreatedAt   time.Time       `json:"created_at"`
}

type MarketingOutreach struct {
	ID               string          `json:"id"`
	ProspectID       string          `json:"prospect_id"`
	Email            string          `json:"email"`
	DisplayName      string          `json:"display_name"`
	Campaign         string          `json:"campaign"`
	Channel          string          `json:"channel"`
	Subject          string          `json:"subject"`
	TouchNumber      int             `json:"touch_number"`
	Status           string          `json:"status"`
	OccurredAt       time.Time       `json:"occurred_at"`
	DiscordMessageID string          `json:"discord_message_id"`
	Metadata         json.RawMessage `json:"metadata"`
	CreatedAt        time.Time       `json:"created_at"`
}

type MarketingPayload struct {
	Prospect  MarketingProspect `json:"prospect"`
	Event     *MarketingEvent   `json:"event,omitempty"`
	Overwrite bool              `json:"overwrite,omitempty"`
}

type marketingLookup struct {
	Email     string `json:"email"`
	PublicURL string `json:"public_url"`
}

type marketingEventRequest struct {
	ProspectID  string          `json:"prospect_id"`
	Email       string          `json:"email"`
	PublicURL   string          `json:"public_url"`
	Type        string          `json:"type"`
	Channel     string          `json:"channel"`
	Campaign    string          `json:"campaign"`
	Subject     string          `json:"subject"`
	TouchNumber int             `json:"touch_number"`
	OccurredAt  string          `json:"occurred_at"`
	Metadata    json.RawMessage `json:"metadata"`
}

type marketingProspectPatch struct {
	Email             *string  `json:"email"`
	DisplayName       *string  `json:"display_name"`
	Persona           *string  `json:"persona"`
	EngineFamiliarity *string  `json:"engine_familiarity"`
	Need              *string  `json:"need"`
	ProjectName       *string  `json:"project_name"`
	PublicURL         *string  `json:"public_url"`
	LastShipDate      *string  `json:"last_ship_date"`
	Tone              *string  `json:"tone"`
	Source            *string  `json:"source"`
	SourceURL         *string  `json:"source_url"`
	LegalOK           *bool    `json:"legal_ok"`
	Status            *string  `json:"status"`
	Campaign          *string  `json:"campaign"`
	Tags              *[]string `json:"tags"`
	Notes             *string  `json:"notes"`
}

type prospectFilters struct {
	Status    string
	Persona   string
	Need      string
	Campaign  string
	Email     string
	PublicURL string
	Limit     int
}

type outreachFilters struct {
	Email      string
	ProspectID string
	Campaign   string
	Limit      int
}

func loadEnvFiles() {
	_ = godotenv.Load()
	_ = godotenv.Load(".env")
	_ = godotenv.Load("../.env")
}

func loadMarketingConfig() {
	marketingCfg.token = strings.TrimSpace(os.Getenv("GROKBOT_ACCESS_TOKEN"))
	marketingCfg.connString = strings.TrimSpace(os.Getenv("POSTGRES_CON_STRING"))
	marketingCfg.guildID = strings.TrimSpace(os.Getenv("MARKETING_DISCORD_GUILD_ID"))
	if marketingCfg.guildID == "" {
		marketingCfg.guildID = defaultMarketingGuildID
	}
	marketingCfg.channelID = strings.TrimSpace(os.Getenv("MARKETING_DISCORD_CHANNEL_ID"))
	if marketingCfg.channelID == "" {
		marketingCfg.channelID = defaultMarketingChannelID
	}
}

func marketingAPIReady() bool {
	return len(marketingCfg.token) == 128 && marketingStore != nil
}

func newMarketingID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("%x", b)
}

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func normalizeURL(u string) string {
	return strings.ToLower(strings.TrimSpace(u))
}

func cloneStrings(in []string) []string {
	if in == nil {
		return []string{}
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}

func normalizeMetadata(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "null" {
		return json.RawMessage(`{}`)
	}
	if !json.Valid(raw) {
		return json.RawMessage(`{}`)
	}
	return raw
}

func oneOf(value string, allowed []string) bool {
	for _, item := range allowed {
		if value == item {
			return true
		}
	}
	return false
}

func validateProspectInput(p *MarketingProspect) error {
	p.Email = strings.TrimSpace(p.Email)
	p.DisplayName = strings.TrimSpace(p.DisplayName)
	p.Persona = strings.TrimSpace(p.Persona)
	p.EngineFamiliarity = strings.ToLower(strings.TrimSpace(p.EngineFamiliarity))
	p.Need = strings.ToLower(strings.TrimSpace(p.Need))
	p.ProjectName = strings.TrimSpace(p.ProjectName)
	p.PublicURL = strings.TrimSpace(p.PublicURL)
	p.LastShipDate = strings.TrimSpace(p.LastShipDate)
	p.Tone = strings.TrimSpace(p.Tone)
	p.Source = strings.TrimSpace(p.Source)
	p.SourceURL = strings.TrimSpace(p.SourceURL)
	p.Status = strings.ToLower(strings.TrimSpace(p.Status))
	p.Campaign = strings.TrimSpace(p.Campaign)
	p.Notes = strings.TrimSpace(p.Notes)
	p.Tags = cloneStrings(p.Tags)

	if p.Email == "" && p.PublicURL == "" {
		return errors.New("prospect requires email or public_url")
	}
	if p.Persona == "" {
		return errors.New("prospect.persona is required")
	}
	if p.Source == "" {
		return errors.New("prospect.source is required")
	}
	if p.EngineFamiliarity != "" && !oneOf(p.EngineFamiliarity, engineFamiliarityValues) {
		return fmt.Errorf("prospect.engine_familiarity must be one of %s", strings.Join(engineFamiliarityValues, ", "))
	}
	if p.Need != "" && !oneOf(p.Need, needValues) {
		return fmt.Errorf("prospect.need must be one of %s", strings.Join(needValues, ", "))
	}
	if p.Status == "" {
		p.Status = "new"
	}
	if !oneOf(p.Status, statusValues) {
		return fmt.Errorf("prospect.status must be one of %s", strings.Join(statusValues, ", "))
	}
	if p.LastShipDate != "" {
		if _, err := time.Parse("2006-01-02", p.LastShipDate); err != nil {
			return errors.New("prospect.last_ship_date must be YYYY-MM-DD")
		}
	}
	if p.Tags == nil {
		p.Tags = []string{}
	}
	return nil
}

func validateEventInput(e *MarketingEvent) error {
	if e == nil {
		return errors.New("event is required")
	}
	e.Type = strings.ToLower(strings.TrimSpace(e.Type))
	e.Channel = strings.TrimSpace(e.Channel)
	e.Campaign = strings.TrimSpace(e.Campaign)
	e.Subject = strings.TrimSpace(e.Subject)
	e.Metadata = normalizeMetadata(e.Metadata)
	if !oneOf(e.Type, eventTypeValues) {
		return fmt.Errorf("event.type must be one of %s", strings.Join(eventTypeValues, ", "))
	}
	if e.OccurredAt.IsZero() {
		e.OccurredAt = time.Now().UTC()
	} else {
		e.OccurredAt = e.OccurredAt.UTC()
	}
	if e.TouchNumber < 0 {
		return errors.New("event.touch_number cannot be negative")
	}
	return nil
}

func applyProspectPatch(p *MarketingProspect, patch marketingProspectPatch) error {
	if patch.Email != nil {
		p.Email = strings.TrimSpace(*patch.Email)
	}
	if patch.DisplayName != nil {
		p.DisplayName = strings.TrimSpace(*patch.DisplayName)
	}
	if patch.Persona != nil {
		p.Persona = strings.TrimSpace(*patch.Persona)
	}
	if patch.EngineFamiliarity != nil {
		p.EngineFamiliarity = strings.ToLower(strings.TrimSpace(*patch.EngineFamiliarity))
	}
	if patch.Need != nil {
		p.Need = strings.ToLower(strings.TrimSpace(*patch.Need))
	}
	if patch.ProjectName != nil {
		p.ProjectName = strings.TrimSpace(*patch.ProjectName)
	}
	if patch.PublicURL != nil {
		p.PublicURL = strings.TrimSpace(*patch.PublicURL)
	}
	if patch.LastShipDate != nil {
		p.LastShipDate = strings.TrimSpace(*patch.LastShipDate)
	}
	if patch.Tone != nil {
		p.Tone = strings.TrimSpace(*patch.Tone)
	}
	if patch.Source != nil {
		p.Source = strings.TrimSpace(*patch.Source)
	}
	if patch.SourceURL != nil {
		p.SourceURL = strings.TrimSpace(*patch.SourceURL)
	}
	if patch.LegalOK != nil {
		p.LegalOK = *patch.LegalOK
	}
	if patch.Status != nil {
		p.Status = strings.ToLower(strings.TrimSpace(*patch.Status))
	}
	if patch.Campaign != nil {
		p.Campaign = strings.TrimSpace(*patch.Campaign)
	}
	if patch.Tags != nil {
		p.Tags = cloneStrings(*patch.Tags)
	}
	if patch.Notes != nil {
		p.Notes = strings.TrimSpace(*patch.Notes)
	}
	return validateProspectInput(p)
}

func copyProspectFields(dst, src *MarketingProspect) {
	dst.Email = src.Email
	dst.DisplayName = src.DisplayName
	dst.Persona = src.Persona
	dst.EngineFamiliarity = src.EngineFamiliarity
	dst.Need = src.Need
	dst.ProjectName = src.ProjectName
	dst.PublicURL = src.PublicURL
	dst.LastShipDate = src.LastShipDate
	dst.Tone = src.Tone
	dst.Source = src.Source
	dst.SourceURL = src.SourceURL
	dst.LegalOK = src.LegalOK
	dst.Status = src.Status
	dst.Campaign = src.Campaign
	dst.Tags = cloneStrings(src.Tags)
	dst.Notes = src.Notes
}

func cloneProspect(p *MarketingProspect) *MarketingProspect {
	if p == nil {
		return nil
	}
	cp := *p
	cp.Tags = cloneStrings(p.Tags)
	if p.LastEventAt != nil {
		t := *p.LastEventAt
		cp.LastEventAt = &t
	}
	if p.LastOutreachAt != nil {
		t := *p.LastOutreachAt
		cp.LastOutreachAt = &t
	}
	return &cp
}

func statusAfterEvent(current, eventType string) string {
	switch eventType {
	case "send":
		if current == "unsubscribed" || current == "excluded" || current == "replied" {
			return current
		}
		return "contacted"
	case "reply":
		return "replied"
	case "bounce":
		if current == "unsubscribed" || current == "excluded" {
			return current
		}
		return "bounced"
	case "unsubscribe":
		return "unsubscribed"
	case "exclude":
		return "excluded"
	case "review":
		if current == "unsubscribed" || current == "excluded" {
			return current
		}
		return "reviewed"
	default:
		return current
	}
}

func parseOccurredAt(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Now().UTC(), nil
	}
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t.UTC(), nil
	}
	if t, err := time.Parse("2006-01-02", value); err == nil {
		return t.UTC(), nil
	}
	return time.Time{}, errors.New("occurred_at must be RFC3339 or YYYY-MM-DD")
}

func eventFromRequest(req marketingEventRequest) (*MarketingEvent, error) {
	occurred, err := parseOccurredAt(req.OccurredAt)
	if err != nil {
		return nil, err
	}
	e := &MarketingEvent{
		ProspectID:  strings.TrimSpace(req.ProspectID),
		Type:        req.Type,
		Channel:     req.Channel,
		Campaign:    req.Campaign,
		Subject:     req.Subject,
		TouchNumber: req.TouchNumber,
		OccurredAt:  occurred,
		Metadata:    req.Metadata,
	}
	if err := validateEventInput(e); err != nil {
		return nil, err
	}
	return e, nil
}

func clampLimit(limit, fallback, max int) int {
	if limit <= 0 {
		return fallback
	}
	if limit > max {
		return max
	}
	return limit
}
