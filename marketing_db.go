package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"
)

const (
	botTablePrefix  = "bot_"
	tableProspects  = botTablePrefix + "marketing_prospects"
	tableEvents     = botTablePrefix + "marketing_events"
	tableOutreach   = botTablePrefix + "marketing_outreach"
)

const prospectColumns = `id, email, display_name, persona, engine_familiarity, need, project_name, public_url, last_ship_date, tone, source, source_url, legal_ok, status, campaign, tags, notes, touch_count, last_event_at, last_outreach_at, created_at, updated_at`

const eventColumns = `id, prospect_id, type, channel, campaign, subject, touch_number, occurred_at, metadata, created_at`

const outreachColumns = `id, prospect_id, email, display_name, campaign, channel, subject, touch_number, status, occurred_at, discord_message_id, metadata, created_at`

type pgMarketingStore struct {
	db *sql.DB
}

func newPostgresMarketingStore(connString string) (*pgMarketingStore, error) {
	db, err := sql.Open("pgx", connString)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(30 * time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	store := &pgMarketingStore{db: db}
	if err := store.ensureSchema(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *pgMarketingStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *pgMarketingStore) ensureSchema(ctx context.Context) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS ` + tableProspects + ` (
			id TEXT PRIMARY KEY,
			email TEXT,
			display_name TEXT NOT NULL DEFAULT '',
			persona TEXT NOT NULL,
			engine_familiarity TEXT NOT NULL DEFAULT '',
			need TEXT NOT NULL DEFAULT '',
			project_name TEXT NOT NULL DEFAULT '',
			public_url TEXT,
			last_ship_date DATE,
			tone TEXT NOT NULL DEFAULT '',
			source TEXT NOT NULL,
			source_url TEXT NOT NULL DEFAULT '',
			legal_ok BOOLEAN NOT NULL DEFAULT FALSE,
			status TEXT NOT NULL DEFAULT 'new',
			campaign TEXT NOT NULL DEFAULT '',
			tags JSONB NOT NULL DEFAULT '[]',
			notes TEXT NOT NULL DEFAULT '',
			touch_count INT NOT NULL DEFAULT 0,
			last_event_at TIMESTAMPTZ,
			last_outreach_at TIMESTAMPTZ,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS ` + tableProspects + `_email_uq
			ON ` + tableProspects + ` (lower(trim(email)))
			WHERE email IS NOT NULL AND trim(email) <> ''`,
		`CREATE UNIQUE INDEX IF NOT EXISTS ` + tableProspects + `_url_uq
			ON ` + tableProspects + ` (lower(public_url))
			WHERE (email IS NULL OR trim(email) = '') AND public_url IS NOT NULL AND public_url <> ''`,
		`CREATE TABLE IF NOT EXISTS ` + tableEvents + ` (
			id TEXT PRIMARY KEY,
			prospect_id TEXT NOT NULL REFERENCES ` + tableProspects + `(id) ON DELETE CASCADE,
			type TEXT NOT NULL,
			channel TEXT NOT NULL DEFAULT '',
			campaign TEXT NOT NULL DEFAULT '',
			subject TEXT NOT NULL DEFAULT '',
			touch_number INT NOT NULL DEFAULT 0,
			occurred_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			metadata JSONB NOT NULL DEFAULT '{}',
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`CREATE INDEX IF NOT EXISTS ` + tableEvents + `_prospect_idx ON ` + tableEvents + ` (prospect_id, occurred_at DESC)`,
		`CREATE TABLE IF NOT EXISTS ` + tableOutreach + ` (
			id TEXT PRIMARY KEY,
			prospect_id TEXT NOT NULL REFERENCES ` + tableProspects + `(id) ON DELETE CASCADE,
			email TEXT NOT NULL DEFAULT '',
			display_name TEXT NOT NULL DEFAULT '',
			campaign TEXT NOT NULL DEFAULT '',
			channel TEXT NOT NULL DEFAULT '',
			subject TEXT NOT NULL DEFAULT '',
			touch_number INT NOT NULL DEFAULT 0,
			status TEXT NOT NULL DEFAULT '',
			occurred_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			discord_message_id TEXT NOT NULL DEFAULT '',
			metadata JSONB NOT NULL DEFAULT '{}',
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`CREATE INDEX IF NOT EXISTS ` + tableOutreach + `_email_idx ON ` + tableOutreach + ` (lower(email), occurred_at DESC)`,
		`CREATE INDEX IF NOT EXISTS ` + tableOutreach + `_prospect_idx ON ` + tableOutreach + ` (prospect_id, occurred_at DESC)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("ensure schema: %w", err)
		}
	}
	return nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func nullString(v string) sql.NullString {
	v = strings.TrimSpace(v)
	if v == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: v, Valid: true}
}

func nullDate(v string) (sql.NullTime, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return sql.NullTime{}, nil
	}
	t, err := time.Parse("2006-01-02", v)
	if err != nil {
		return sql.NullTime{}, err
	}
	return sql.NullTime{Time: t, Valid: true}, nil
}

func nullTimePtr(t *time.Time) sql.NullTime {
	if t == nil || t.IsZero() {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: t.UTC(), Valid: true}
}

func scanProspect(scanner interface {
	Scan(dest ...any) error
}) (*MarketingProspect, error) {
	var (
		p                         MarketingProspect
		email, publicURL          sql.NullString
		lastShip                  sql.NullTime
		lastEvent, lastOutreach   sql.NullTime
		tagsJSON                  []byte
	)
	err := scanner.Scan(
		&p.ID,
		&email,
		&p.DisplayName,
		&p.Persona,
		&p.EngineFamiliarity,
		&p.Need,
		&p.ProjectName,
		&publicURL,
		&lastShip,
		&p.Tone,
		&p.Source,
		&p.SourceURL,
		&p.LegalOK,
		&p.Status,
		&p.Campaign,
		&tagsJSON,
		&p.Notes,
		&p.TouchCount,
		&lastEvent,
		&lastOutreach,
		&p.CreatedAt,
		&p.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	if email.Valid {
		p.Email = email.String
	}
	if publicURL.Valid {
		p.PublicURL = publicURL.String
	}
	if lastShip.Valid {
		p.LastShipDate = lastShip.Time.UTC().Format("2006-01-02")
	}
	if lastEvent.Valid {
		t := lastEvent.Time.UTC()
		p.LastEventAt = &t
	}
	if lastOutreach.Valid {
		t := lastOutreach.Time.UTC()
		p.LastOutreachAt = &t
	}
	if len(tagsJSON) > 0 {
		_ = json.Unmarshal(tagsJSON, &p.Tags)
	}
	if p.Tags == nil {
		p.Tags = []string{}
	}
	p.CreatedAt = p.CreatedAt.UTC()
	p.UpdatedAt = p.UpdatedAt.UTC()
	return &p, nil
}

func scanEvent(scanner interface {
	Scan(dest ...any) error
}) (*MarketingEvent, error) {
	var e MarketingEvent
	var metadata []byte
	if err := scanner.Scan(
		&e.ID,
		&e.ProspectID,
		&e.Type,
		&e.Channel,
		&e.Campaign,
		&e.Subject,
		&e.TouchNumber,
		&e.OccurredAt,
		&metadata,
		&e.CreatedAt,
	); err != nil {
		return nil, err
	}
	e.Metadata = normalizeMetadata(metadata)
	e.OccurredAt = e.OccurredAt.UTC()
	e.CreatedAt = e.CreatedAt.UTC()
	return &e, nil
}

func scanOutreach(scanner interface {
	Scan(dest ...any) error
}) (*MarketingOutreach, error) {
	var o MarketingOutreach
	var metadata []byte
	if err := scanner.Scan(
		&o.ID,
		&o.ProspectID,
		&o.Email,
		&o.DisplayName,
		&o.Campaign,
		&o.Channel,
		&o.Subject,
		&o.TouchNumber,
		&o.Status,
		&o.OccurredAt,
		&o.DiscordMessageID,
		&metadata,
		&o.CreatedAt,
	); err != nil {
		return nil, err
	}
	o.Metadata = normalizeMetadata(metadata)
	o.OccurredAt = o.OccurredAt.UTC()
	o.CreatedAt = o.CreatedAt.UTC()
	return &o, nil
}

func (s *pgMarketingStore) Lookup(email, publicURL string) (*MarketingProspect, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	email = normalizeEmail(email)
	publicURL = strings.TrimSpace(publicURL)
	if email != "" {
		row := s.db.QueryRowContext(ctx, `SELECT `+prospectColumns+` FROM `+tableProspects+` WHERE lower(trim(email)) = $1`, email)
		p, err := scanProspect(row)
		if err == nil {
			return p, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}
	if publicURL != "" {
		row := s.db.QueryRowContext(ctx, `SELECT `+prospectColumns+` FROM `+tableProspects+` WHERE lower(public_url) = lower($1)`, publicURL)
		p, err := scanProspect(row)
		if err == nil {
			return p, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}
	return nil, nil
}

func (s *pgMarketingStore) InsertProspect(p *MarketingProspect) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if p.ID == "" {
		p.ID = newMarketingID()
	}
	now := time.Now().UTC()
	if p.CreatedAt.IsZero() {
		p.CreatedAt = now
	}
	p.UpdatedAt = now
	tags, err := json.Marshal(cloneStrings(p.Tags))
	if err != nil {
		return err
	}
	ship, err := nullDate(p.LastShipDate)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO ` + tableProspects + ` (
			id, email, display_name, persona, engine_familiarity, need, project_name, public_url, last_ship_date,
			tone, source, source_url, legal_ok, status, campaign, tags, notes, touch_count, last_event_at, last_outreach_at, created_at, updated_at
		) VALUES (
			$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22
		)`,
		p.ID,
		nullString(p.Email),
		p.DisplayName,
		p.Persona,
		p.EngineFamiliarity,
		p.Need,
		p.ProjectName,
		nullString(p.PublicURL),
		ship,
		p.Tone,
		p.Source,
		p.SourceURL,
		p.LegalOK,
		p.Status,
		p.Campaign,
		tags,
		p.Notes,
		p.TouchCount,
		nullTimePtr(p.LastEventAt),
		nullTimePtr(p.LastOutreachAt),
		p.CreatedAt,
		p.UpdatedAt,
	)
	if isUniqueViolation(err) {
		return errAlreadyExists
	}
	return err
}

func (s *pgMarketingStore) UpdateProspect(p *MarketingProspect) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p.UpdatedAt = time.Now().UTC()
	tags, err := json.Marshal(cloneStrings(p.Tags))
	if err != nil {
		return err
	}
	ship, err := nullDate(p.LastShipDate)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE ` + tableProspects + ` SET
			email=$2, display_name=$3, persona=$4, engine_familiarity=$5, need=$6, project_name=$7, public_url=$8,
			last_ship_date=$9, tone=$10, source=$11, source_url=$12, legal_ok=$13, status=$14, campaign=$15, tags=$16,
			notes=$17, touch_count=$18, last_event_at=$19, last_outreach_at=$20, updated_at=$21
		WHERE id=$1`,
		p.ID,
		nullString(p.Email),
		p.DisplayName,
		p.Persona,
		p.EngineFamiliarity,
		p.Need,
		p.ProjectName,
		nullString(p.PublicURL),
		ship,
		p.Tone,
		p.Source,
		p.SourceURL,
		p.LegalOK,
		p.Status,
		p.Campaign,
		tags,
		p.Notes,
		p.TouchCount,
		nullTimePtr(p.LastEventAt),
		nullTimePtr(p.LastOutreachAt),
		p.UpdatedAt,
	)
	if isUniqueViolation(err) {
		return errAlreadyExists
	}
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errNotFound
	}
	return nil
}

func (s *pgMarketingStore) GetProspect(id string) (*MarketingProspect, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	row := s.db.QueryRowContext(ctx, `SELECT `+prospectColumns+` FROM `+tableProspects+` WHERE id=$1`, id)
	p, err := scanProspect(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound
	}
	return p, err
}

func (s *pgMarketingStore) ListProspects(filters prospectFilters) ([]MarketingProspect, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	limit := clampLimit(filters.Limit, 50, 200)
	q := `SELECT ` + prospectColumns + ` FROM ` + tableProspects + ` WHERE 1=1`
	args := make([]any, 0, 8)
	n := 1
	if filters.Status != "" {
		q += fmt.Sprintf(` AND status=$%d`, n)
		args = append(args, filters.Status)
		n++
	}
	if filters.Persona != "" {
		q += fmt.Sprintf(` AND persona=$%d`, n)
		args = append(args, filters.Persona)
		n++
	}
	if filters.Need != "" {
		q += fmt.Sprintf(` AND need=$%d`, n)
		args = append(args, filters.Need)
		n++
	}
	if filters.Campaign != "" {
		q += fmt.Sprintf(` AND campaign=$%d`, n)
		args = append(args, filters.Campaign)
		n++
	}
	if filters.Email != "" {
		q += fmt.Sprintf(` AND lower(trim(email))=lower(trim($%d))`, n)
		args = append(args, filters.Email)
		n++
	}
	if filters.PublicURL != "" {
		q += fmt.Sprintf(` AND lower(public_url)=lower($%d)`, n)
		args = append(args, filters.PublicURL)
		n++
	}
	q += fmt.Sprintf(` ORDER BY updated_at DESC LIMIT $%d`, n)
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]MarketingProspect, 0)
	for rows.Next() {
		p, err := scanProspect(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func (s *pgMarketingStore) InsertEvent(e *MarketingEvent) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if e.ID == "" {
		e.ID = newMarketingID()
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now().UTC()
	}
	e.Metadata = normalizeMetadata(e.Metadata)
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO ` + tableEvents + ` (` + eventColumns + `)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		e.ID, e.ProspectID, e.Type, e.Channel, e.Campaign, e.Subject, e.TouchNumber, e.OccurredAt, []byte(e.Metadata), e.CreatedAt,
	)
	return err
}

func (s *pgMarketingStore) ListEvents(prospectID, email string, limit int) ([]MarketingEvent, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	limit = clampLimit(limit, 50, 200)
	q := `SELECT e.id, e.prospect_id, e.type, e.channel, e.campaign, e.subject, e.touch_number, e.occurred_at, e.metadata, e.created_at
		FROM ` + tableEvents + ` e
		JOIN ` + tableProspects + ` p ON p.id = e.prospect_id
		WHERE 1=1`
	args := make([]any, 0, 4)
	n := 1
	if prospectID != "" {
		q += fmt.Sprintf(` AND e.prospect_id=$%d`, n)
		args = append(args, prospectID)
		n++
	}
	if email != "" {
		q += fmt.Sprintf(` AND lower(trim(p.email))=lower(trim($%d))`, n)
		args = append(args, email)
		n++
	}
	q += fmt.Sprintf(` ORDER BY e.occurred_at DESC LIMIT $%d`, n)
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]MarketingEvent, 0)
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

func (s *pgMarketingStore) InsertOutreach(o *MarketingOutreach) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if o.ID == "" {
		o.ID = newMarketingID()
	}
	if o.CreatedAt.IsZero() {
		o.CreatedAt = time.Now().UTC()
	}
	o.Metadata = normalizeMetadata(o.Metadata)
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO ` + tableOutreach + ` (` + outreachColumns + `)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		o.ID, o.ProspectID, o.Email, o.DisplayName, o.Campaign, o.Channel, o.Subject, o.TouchNumber, o.Status, o.OccurredAt, o.DiscordMessageID, []byte(o.Metadata), o.CreatedAt,
	)
	return err
}

func (s *pgMarketingStore) UpdateOutreachDiscordID(id, messageID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := s.db.ExecContext(ctx, `UPDATE `+tableOutreach+` SET discord_message_id=$2 WHERE id=$1`, id, messageID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errNotFound
	}
	return nil
}

func (s *pgMarketingStore) ListOutreach(filters outreachFilters) ([]MarketingOutreach, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	limit := clampLimit(filters.Limit, 50, 200)
	q := `SELECT ` + outreachColumns + ` FROM ` + tableOutreach + ` WHERE 1=1`
	args := make([]any, 0, 4)
	n := 1
	if filters.ProspectID != "" {
		q += fmt.Sprintf(` AND prospect_id=$%d`, n)
		args = append(args, filters.ProspectID)
		n++
	}
	if filters.Email != "" {
		q += fmt.Sprintf(` AND lower(email)=lower($%d)`, n)
		args = append(args, filters.Email)
		n++
	}
	if filters.Campaign != "" {
		q += fmt.Sprintf(` AND campaign=$%d`, n)
		args = append(args, filters.Campaign)
		n++
	}
	q += fmt.Sprintf(` ORDER BY occurred_at DESC LIMIT $%d`, n)
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]MarketingOutreach, 0)
	for rows.Next() {
		o, err := scanOutreach(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *o)
	}
	return out, rows.Err()
}

func (s *pgMarketingStore) groupedCount(ctx context.Context, query string) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var key string
		var n int
		if err := rows.Scan(&key, &n); err != nil {
			return nil, err
		}
		if strings.TrimSpace(key) == "" {
			key = "(none)"
		}
		out[key] = n
	}
	return out, rows.Err()
}

func (s *pgMarketingStore) Stats() (*MarketingStats, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	st := newMarketingStats()
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+tableProspects).Scan(&st.Prospects); err != nil {
		return nil, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+tableOutreach).Scan(&st.Outreach); err != nil {
		return nil, err
	}
	var last sql.NullTime
	if err := s.db.QueryRowContext(ctx, `SELECT MAX(occurred_at) FROM `+tableOutreach).Scan(&last); err != nil {
		return nil, err
	}
	if last.Valid {
		t := last.Time.UTC()
		st.LastOutreachAt = &t
	}
	var err error
	if st.ByStatus, err = s.groupedCount(ctx, `SELECT COALESCE(status, ''), COUNT(*) FROM `+tableProspects+` GROUP BY 1`); err != nil {
		return nil, err
	}
	if st.ByNeed, err = s.groupedCount(ctx, `SELECT COALESCE(need, ''), COUNT(*) FROM `+tableProspects+` GROUP BY 1`); err != nil {
		return nil, err
	}
	if st.ByPersona, err = s.groupedCount(ctx, `SELECT COALESCE(persona, ''), COUNT(*) FROM `+tableProspects+` GROUP BY 1`); err != nil {
		return nil, err
	}
	if st.ByCampaign, err = s.groupedCount(ctx, `SELECT COALESCE(campaign, ''), COUNT(*) FROM `+tableProspects+` GROUP BY 1`); err != nil {
		return nil, err
	}
	if st.EventsByType, err = s.groupedCount(ctx, `SELECT COALESCE(type, ''), COUNT(*) FROM `+tableEvents+` GROUP BY 1`); err != nil {
		return nil, err
	}
	return st, nil
}
