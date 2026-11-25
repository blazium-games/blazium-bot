package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

const (
	tableTicketTypes     = botTablePrefix + "ticket_types"
	tableTicketQuestions = botTablePrefix + "ticket_questions"
	tableTicketStaff     = botTablePrefix + "ticket_staff"
	tableTickets         = botTablePrefix + "tickets"
)

type pgTicketsStore struct {
	db *sql.DB
}

func newPostgresTicketsStore(db *sql.DB) (*pgTicketsStore, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := ensureTables(ctx, db, []string{
		`CREATE TABLE IF NOT EXISTS ` + tableTicketTypes + ` (
			id BIGSERIAL PRIMARY KEY,
			guild_id TEXT NOT NULL,
			name TEXT NOT NULL,
			intro TEXT NOT NULL DEFAULT '',
			category_id TEXT NOT NULL,
			log_channel_id TEXT NOT NULL,
			open BOOLEAN NOT NULL DEFAULT TRUE,
			emoji TEXT NOT NULL DEFAULT '',
			header TEXT NOT NULL DEFAULT '',
			welcome TEXT NOT NULL DEFAULT '',
			max_open INTEGER NOT NULL DEFAULT 1,
			dm_transcript BOOLEAN NOT NULL DEFAULT TRUE,
			ping_staff BOOLEAN NOT NULL DEFAULT FALSE,
			created_by TEXT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`CREATE INDEX IF NOT EXISTS ` + tableTicketTypes + `_guild_idx ON ` + tableTicketTypes + ` (guild_id)`,
		`CREATE TABLE IF NOT EXISTS ` + tableTicketQuestions + ` (
			id BIGSERIAL PRIMARY KEY,
			type_id BIGINT NOT NULL REFERENCES ` + tableTicketTypes + `(id) ON DELETE CASCADE,
			position INTEGER NOT NULL,
			label TEXT NOT NULL,
			kind TEXT NOT NULL,
			required BOOLEAN NOT NULL DEFAULT TRUE,
			help TEXT NOT NULL DEFAULT '',
			choices JSONB NOT NULL DEFAULT '[]',
			max_length INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE INDEX IF NOT EXISTS ` + tableTicketQuestions + `_type_idx ON ` + tableTicketQuestions + ` (type_id, position)`,
		`CREATE TABLE IF NOT EXISTS ` + tableTicketStaff + ` (
			type_id BIGINT NOT NULL REFERENCES ` + tableTicketTypes + `(id) ON DELETE CASCADE,
			role_id TEXT NOT NULL,
			added_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			PRIMARY KEY (type_id, role_id)
		)`,
		`CREATE TABLE IF NOT EXISTS ` + tableTickets + ` (
			id BIGSERIAL PRIMARY KEY,
			guild_id TEXT NOT NULL,
			number BIGINT NOT NULL,
			type_id BIGINT NOT NULL REFERENCES ` + tableTicketTypes + `(id) ON DELETE CASCADE,
			user_id TEXT NOT NULL,
			channel_id TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'open',
			answers JSONB NOT NULL DEFAULT '[]',
			opened_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			claimed_by TEXT NOT NULL DEFAULT '',
			closed_by TEXT NOT NULL DEFAULT '',
			close_reason TEXT NOT NULL DEFAULT '',
			closed_at TIMESTAMPTZ,
			UNIQUE (guild_id, number)
		)`,
		`CREATE INDEX IF NOT EXISTS ` + tableTickets + `_channel_idx ON ` + tableTickets + ` (channel_id)`,
		`CREATE INDEX IF NOT EXISTS ` + tableTickets + `_user_idx ON ` + tableTickets + ` (type_id, user_id, status)`,
	})
	if err != nil {
		return nil, err
	}
	return &pgTicketsStore{db: db}, nil
}

const ticketTypeColumns = `id, guild_id, name, intro, category_id, log_channel_id, open, emoji, header, welcome, max_open, dm_transcript, ping_staff, created_by, created_at`

func scanTicketType(row interface{ Scan(...any) error }) (*ticketType, error) {
	var t ticketType
	err := row.Scan(&t.ID, &t.GuildID, &t.Name, &t.Intro, &t.CategoryID, &t.LogChannelID, &t.Open, &t.Emoji, &t.Header,
		&t.Welcome, &t.MaxOpen, &t.DMTranscript, &t.PingStaff, &t.CreatedBy, &t.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (s *pgTicketsStore) CreateType(t *ticketType) (int64, error) {
	ctx, cancel := dbCtx()
	defer cancel()
	var id int64
	err := s.db.QueryRowContext(ctx, `INSERT INTO `+tableTicketTypes+` (guild_id, name, intro, category_id, log_channel_id, open,
		emoji, header, welcome, max_open, dm_transcript, ping_staff, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13) RETURNING id`,
		t.GuildID, t.Name, t.Intro, t.CategoryID, t.LogChannelID, t.Open, t.Emoji, t.Header, t.Welcome, t.MaxOpen,
		t.DMTranscript, t.PingStaff, t.CreatedBy).Scan(&id)
	return id, err
}

func (s *pgTicketsStore) GetType(guildID string, id int64) (*ticketType, error) {
	ctx, cancel := dbCtx()
	defer cancel()
	t, err := scanTicketType(s.db.QueryRowContext(ctx, `SELECT `+ticketTypeColumns+` FROM `+tableTicketTypes+` WHERE id = $1 AND guild_id = $2`, id, guildID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return t, err
}

func (s *pgTicketsStore) ListTypes(guildID string) ([]ticketType, error) {
	ctx, cancel := dbCtx()
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `SELECT `+ticketTypeColumns+` FROM `+tableTicketTypes+` WHERE guild_id = $1 ORDER BY id`, guildID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ticketType{}
	for rows.Next() {
		t, err := scanTicketType(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

func (s *pgTicketsStore) UpdateType(t *ticketType) error {
	ctx, cancel := dbCtx()
	defer cancel()
	_, err := s.db.ExecContext(ctx, `UPDATE `+tableTicketTypes+` SET name = $2, intro = $3, category_id = $4, log_channel_id = $5,
		open = $6, emoji = $7, header = $8, welcome = $9, max_open = $10, dm_transcript = $11, ping_staff = $12 WHERE id = $1`,
		t.ID, t.Name, t.Intro, t.CategoryID, t.LogChannelID, t.Open, t.Emoji, t.Header, t.Welcome, t.MaxOpen, t.DMTranscript, t.PingStaff)
	return err
}

func (s *pgTicketsStore) DeleteType(id int64) error {
	ctx, cancel := dbCtx()
	defer cancel()
	// Questions, staff roles and ticket records go with it (ON DELETE CASCADE).
	_, err := s.db.ExecContext(ctx, `DELETE FROM `+tableTicketTypes+` WHERE id = $1`, id)
	return err
}

func (s *pgTicketsStore) Questions(typeID int64) ([]formQuestion, error) {
	ctx, cancel := dbCtx()
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `SELECT id, label, kind, required, help, choices, max_length FROM `+
		tableTicketQuestions+` WHERE type_id = $1 ORDER BY position, id`, typeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []formQuestion{}
	for rows.Next() {
		var q formQuestion
		var choices []byte
		if err := rows.Scan(&q.ID, &q.Label, &q.Kind, &q.Required, &q.Help, &choices, &q.MaxLength); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(choices, &q.Choices)
		out = append(out, q)
	}
	return out, rows.Err()
}

func (s *pgTicketsStore) AddQuestion(typeID int64, q formQuestion) error {
	ctx, cancel := dbCtx()
	defer cancel()
	choices, _ := json.Marshal(append([]string{}, q.Choices...))
	_, err := s.db.ExecContext(ctx, `INSERT INTO `+tableTicketQuestions+` (type_id, position, label, kind, required, help, choices, max_length)
		VALUES ($1, (SELECT COALESCE(MAX(position) + 1, 0) FROM `+tableTicketQuestions+` WHERE type_id = $1), $2, $3, $4, $5, $6, $7)`,
		typeID, q.Label, q.Kind, q.Required, q.Help, string(choices), q.MaxLength)
	return err
}

func (s *pgTicketsStore) RemoveQuestion(typeID, questionID int64) error {
	ctx, cancel := dbCtx()
	defer cancel()
	_, err := s.db.ExecContext(ctx, `DELETE FROM `+tableTicketQuestions+` WHERE id = $1 AND type_id = $2`, questionID, typeID)
	return err
}

func (s *pgTicketsStore) ReorderQuestions(typeID int64, ids []int64) error {
	ctx, cancel := dbCtx()
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for position, id := range ids {
		if _, err := tx.ExecContext(ctx, `UPDATE `+tableTicketQuestions+` SET position = $1 WHERE id = $2 AND type_id = $3`,
			position, id, typeID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *pgTicketsStore) StaffRoles(typeID int64) ([]string, error) {
	ctx, cancel := dbCtx()
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `SELECT role_id FROM `+tableTicketStaff+` WHERE type_id = $1 ORDER BY added_at, role_id`, typeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (s *pgTicketsStore) AddStaffRole(typeID int64, roleID string) error {
	ctx, cancel := dbCtx()
	defer cancel()
	_, err := s.db.ExecContext(ctx, `INSERT INTO `+tableTicketStaff+` (type_id, role_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, typeID, roleID)
	return err
}

func (s *pgTicketsStore) RemoveStaffRole(typeID int64, roleID string) error {
	ctx, cancel := dbCtx()
	defer cancel()
	_, err := s.db.ExecContext(ctx, `DELETE FROM `+tableTicketStaff+` WHERE type_id = $1 AND role_id = $2`, typeID, roleID)
	return err
}

const ticketColumns = `id, guild_id, number, type_id, user_id, channel_id, status, answers, opened_at, claimed_by, closed_by, close_reason, closed_at`

func scanTicket(row interface{ Scan(...any) error }) (*ticket, error) {
	var t ticket
	var answers []byte
	var closedAt sql.NullTime
	err := row.Scan(&t.ID, &t.GuildID, &t.Number, &t.TypeID, &t.UserID, &t.ChannelID, &t.Status, &answers, &t.OpenedAt,
		&t.ClaimedBy, &t.ClosedBy, &t.CloseReason, &closedAt)
	if err != nil {
		return nil, err
	}
	t.Answers = string(answers)
	if closedAt.Valid {
		t.ClosedAt = &closedAt.Time
	}
	return &t, nil
}

func (s *pgTicketsStore) CreateTicket(t *ticket) (int64, int64, error) {
	answers := t.Answers
	if answers == "" {
		answers = "[]"
	}
	// Two tickets opened at the same moment can pick the same number; the unique
	// (guild_id, number) index refuses the second, which tries again.
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		ctx, cancel := dbCtx()
		var id, number int64
		lastErr = s.db.QueryRowContext(ctx, `INSERT INTO `+tableTickets+` (guild_id, number, type_id, user_id, answers)
			VALUES ($1, (SELECT COALESCE(MAX(number), 0) + 1 FROM `+tableTickets+` WHERE guild_id = $1), $2, $3, $4)
			RETURNING id, number`, t.GuildID, t.TypeID, t.UserID, answers).Scan(&id, &number)
		cancel()
		if lastErr == nil {
			return id, number, nil
		}
		if !isUniqueViolation(lastErr) {
			return 0, 0, lastErr
		}
	}
	return 0, 0, lastErr
}

func (s *pgTicketsStore) GetTicket(guildID string, id int64) (*ticket, error) {
	ctx, cancel := dbCtx()
	defer cancel()
	t, err := scanTicket(s.db.QueryRowContext(ctx, `SELECT `+ticketColumns+` FROM `+tableTickets+` WHERE id = $1 AND guild_id = $2`, id, guildID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return t, err
}

func (s *pgTicketsStore) TicketByChannel(channelID string) (*ticket, error) {
	if channelID == "" {
		return nil, nil
	}
	ctx, cancel := dbCtx()
	defer cancel()
	t, err := scanTicket(s.db.QueryRowContext(ctx, `SELECT `+ticketColumns+` FROM `+tableTickets+
		` WHERE channel_id = $1 ORDER BY id DESC LIMIT 1`, channelID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return t, err
}

func (s *pgTicketsStore) queryTickets(query string, args ...any) ([]ticket, error) {
	ctx, cancel := dbCtx()
	defer cancel()
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ticket{}
	for rows.Next() {
		t, err := scanTicket(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

func (s *pgTicketsStore) ListTickets(guildID string, typeID int64, status string) ([]ticket, error) {
	return s.queryTickets(`SELECT `+ticketColumns+` FROM `+tableTickets+
		` WHERE guild_id = $1 AND ($2 = 0 OR type_id = $2) AND ($3 = '' OR status = $3) ORDER BY id DESC`, guildID, typeID, status)
}

func (s *pgTicketsStore) OpenTicketsOf(typeID int64, userID string) ([]ticket, error) {
	return s.queryTickets(`SELECT `+ticketColumns+` FROM `+tableTickets+
		` WHERE type_id = $1 AND user_id = $2 AND status = 'open' ORDER BY id`, typeID, userID)
}

func (s *pgTicketsStore) exec(query string, args ...any) (int64, error) {
	ctx, cancel := dbCtx()
	defer cancel()
	res, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *pgTicketsStore) SetTicketChannel(id int64, channelID string) error {
	_, err := s.exec(`UPDATE `+tableTickets+` SET channel_id = $2 WHERE id = $1`, id, channelID)
	return err
}

func (s *pgTicketsStore) SetClaim(id int64, userID string) error {
	_, err := s.exec(`UPDATE `+tableTickets+` SET claimed_by = $2 WHERE id = $1`, id, userID)
	return err
}

func (s *pgTicketsStore) Close(id int64, by, reason string) (bool, error) {
	n, err := s.exec(`UPDATE `+tableTickets+` SET status = 'closed', closed_by = $2, close_reason = $3, closed_at = NOW()
		WHERE id = $1 AND status = 'open'`, id, by, reason)
	return n == 1, err
}

func (s *pgTicketsStore) Reopen(id int64) (bool, error) {
	n, err := s.exec(`UPDATE `+tableTickets+` SET status = 'open' WHERE id = $1 AND status = 'closed'`, id)
	return n == 1, err
}

func (s *pgTicketsStore) MarkDeleted(id int64) error {
	_, err := s.exec(`UPDATE `+tableTickets+` SET status = 'deleted' WHERE id = $1`, id)
	return err
}

func (s *pgTicketsStore) DeleteTicket(id int64) error {
	_, err := s.exec(`DELETE FROM `+tableTickets+` WHERE id = $1`, id)
	return err
}
