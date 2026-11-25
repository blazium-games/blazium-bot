package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

const (
	tableForms         = botTablePrefix + "forms"
	tableFormQuestions = botTablePrefix + "form_questions"
	tableFormEntries   = botTablePrefix + "form_entries"
	tableFormVotes     = botTablePrefix + "form_votes"
	tableFormChatStaff = botTablePrefix + "form_chat_staff"
)

type pgFormsStore struct {
	db *sql.DB
}

func newPostgresFormsStore(db *sql.DB) (*pgFormsStore, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := ensureTables(ctx, db, []string{
		`CREATE TABLE IF NOT EXISTS ` + tableForms + ` (
			id BIGSERIAL PRIMARY KEY,
			guild_id TEXT NOT NULL,
			name TEXT NOT NULL,
			intro TEXT NOT NULL DEFAULT '',
			review_channel_id TEXT NOT NULL,
			accept_role_id TEXT NOT NULL DEFAULT '',
			reviewer_role_id TEXT NOT NULL DEFAULT '',
			open BOOLEAN NOT NULL DEFAULT TRUE,
			chat_category_id TEXT NOT NULL DEFAULT '',
			dm_accept BOOLEAN NOT NULL DEFAULT TRUE,
			dm_decline BOOLEAN NOT NULL DEFAULT TRUE,
			emoji TEXT NOT NULL DEFAULT '',
			header TEXT NOT NULL DEFAULT '',
			welcome TEXT NOT NULL DEFAULT '',
			created_by TEXT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`CREATE INDEX IF NOT EXISTS ` + tableForms + `_guild_idx ON ` + tableForms + ` (guild_id)`,
		`CREATE TABLE IF NOT EXISTS ` + tableFormQuestions + ` (
			id BIGSERIAL PRIMARY KEY,
			form_id BIGINT NOT NULL REFERENCES ` + tableForms + `(id) ON DELETE CASCADE,
			position INTEGER NOT NULL,
			label TEXT NOT NULL,
			kind TEXT NOT NULL,
			required BOOLEAN NOT NULL DEFAULT TRUE,
			help TEXT NOT NULL DEFAULT '',
			choices JSONB NOT NULL DEFAULT '[]',
			max_length INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE INDEX IF NOT EXISTS ` + tableFormQuestions + `_form_idx ON ` + tableFormQuestions + ` (form_id, position)`,
		`CREATE TABLE IF NOT EXISTS ` + tableFormEntries + ` (
			id BIGSERIAL PRIMARY KEY,
			form_id BIGINT NOT NULL REFERENCES ` + tableForms + `(id) ON DELETE CASCADE,
			guild_id TEXT NOT NULL,
			user_id TEXT NOT NULL,
			answers JSONB NOT NULL,
			status TEXT NOT NULL DEFAULT 'pending',
			submitted_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			review_channel_id TEXT NOT NULL DEFAULT '',
			review_message_id TEXT NOT NULL DEFAULT '',
			decided_by TEXT NOT NULL DEFAULT '',
			decided_at TIMESTAMPTZ,
			note TEXT NOT NULL DEFAULT '',
			chat_channel_id TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS ` + tableFormEntries + `_form_idx ON ` + tableFormEntries + ` (form_id, status)`,
		`CREATE INDEX IF NOT EXISTS ` + tableFormEntries + `_user_idx ON ` + tableFormEntries + ` (guild_id, user_id)`,
		// One waiting application per member per form, even when two submits race.
		`CREATE UNIQUE INDEX IF NOT EXISTS ` + tableFormEntries + `_one_pending ON ` + tableFormEntries +
			` (form_id, user_id) WHERE status = 'pending'`,
		`CREATE TABLE IF NOT EXISTS ` + tableFormVotes + ` (
			entry_id BIGINT NOT NULL REFERENCES ` + tableFormEntries + `(id) ON DELETE CASCADE,
			user_id TEXT NOT NULL,
			vote TEXT NOT NULL,
			voted_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			PRIMARY KEY (entry_id, user_id)
		)`,
		`CREATE TABLE IF NOT EXISTS ` + tableFormChatStaff + ` (
			form_id BIGINT NOT NULL REFERENCES ` + tableForms + `(id) ON DELETE CASCADE,
			role_id TEXT NOT NULL,
			added_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			PRIMARY KEY (form_id, role_id)
		)`,
	})
	if err != nil {
		return nil, err
	}
	return &pgFormsStore{db: db}, nil
}

const formColumns = `id, guild_id, name, intro, review_channel_id, accept_role_id, reviewer_role_id, open, chat_category_id, dm_accept, dm_decline, emoji, header, welcome, created_by, created_at`

func scanForm(row interface{ Scan(...any) error }) (*appForm, error) {
	var f appForm
	err := row.Scan(&f.ID, &f.GuildID, &f.Name, &f.Intro, &f.ReviewChannelID, &f.AcceptRoleID, &f.ReviewerRoleID, &f.Open,
		&f.ChatCategoryID, &f.DMAccept, &f.DMDecline, &f.Emoji, &f.Header, &f.Welcome, &f.CreatedBy, &f.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &f, nil
}

func (s *pgFormsStore) CreateForm(f *appForm) (int64, error) {
	ctx, cancel := dbCtx()
	defer cancel()
	var id int64
	err := s.db.QueryRowContext(ctx, `INSERT INTO `+tableForms+` (guild_id, name, intro, review_channel_id, accept_role_id,
		reviewer_role_id, open, chat_category_id, dm_accept, dm_decline, emoji, header, welcome, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14) RETURNING id`,
		f.GuildID, f.Name, f.Intro, f.ReviewChannelID, f.AcceptRoleID, f.ReviewerRoleID, f.Open, f.ChatCategoryID,
		f.DMAccept, f.DMDecline, f.Emoji, f.Header, f.Welcome, f.CreatedBy).Scan(&id)
	return id, err
}

func (s *pgFormsStore) GetForm(guildID string, id int64) (*appForm, error) {
	ctx, cancel := dbCtx()
	defer cancel()
	f, err := scanForm(s.db.QueryRowContext(ctx, `SELECT `+formColumns+` FROM `+tableForms+` WHERE id = $1 AND guild_id = $2`, id, guildID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return f, err
}

func (s *pgFormsStore) ListForms(guildID string) ([]appForm, error) {
	ctx, cancel := dbCtx()
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `SELECT `+formColumns+` FROM `+tableForms+` WHERE guild_id = $1 ORDER BY id`, guildID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []appForm{}
	for rows.Next() {
		f, err := scanForm(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *f)
	}
	return out, rows.Err()
}

func (s *pgFormsStore) UpdateForm(f *appForm) error {
	ctx, cancel := dbCtx()
	defer cancel()
	_, err := s.db.ExecContext(ctx, `UPDATE `+tableForms+` SET name = $2, intro = $3, review_channel_id = $4,
		accept_role_id = $5, reviewer_role_id = $6, open = $7, chat_category_id = $8, dm_accept = $9, dm_decline = $10,
		emoji = $11, header = $12, welcome = $13 WHERE id = $1`,
		f.ID, f.Name, f.Intro, f.ReviewChannelID, f.AcceptRoleID, f.ReviewerRoleID, f.Open, f.ChatCategoryID,
		f.DMAccept, f.DMDecline, f.Emoji, f.Header, f.Welcome)
	return err
}

func (s *pgFormsStore) DeleteForm(id int64) error {
	ctx, cancel := dbCtx()
	defer cancel()
	// Questions, applications, votes and chat roles go with it (ON DELETE CASCADE).
	_, err := s.db.ExecContext(ctx, `DELETE FROM `+tableForms+` WHERE id = $1`, id)
	return err
}

func (s *pgFormsStore) Questions(formID int64) ([]formQuestion, error) {
	ctx, cancel := dbCtx()
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `SELECT id, label, kind, required, help, choices, max_length FROM `+
		tableFormQuestions+` WHERE form_id = $1 ORDER BY position, id`, formID)
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

func (s *pgFormsStore) AddQuestion(formID int64, q formQuestion) error {
	ctx, cancel := dbCtx()
	defer cancel()
	choices, _ := json.Marshal(append([]string{}, q.Choices...))
	_, err := s.db.ExecContext(ctx, `INSERT INTO `+tableFormQuestions+` (form_id, position, label, kind, required, help, choices, max_length)
		VALUES ($1, (SELECT COALESCE(MAX(position) + 1, 0) FROM `+tableFormQuestions+` WHERE form_id = $1), $2, $3, $4, $5, $6, $7)`,
		formID, q.Label, q.Kind, q.Required, q.Help, string(choices), q.MaxLength)
	return err
}

func (s *pgFormsStore) RemoveQuestion(formID, questionID int64) error {
	ctx, cancel := dbCtx()
	defer cancel()
	_, err := s.db.ExecContext(ctx, `DELETE FROM `+tableFormQuestions+` WHERE id = $1 AND form_id = $2`, questionID, formID)
	return err
}

func (s *pgFormsStore) ReorderQuestions(formID int64, ids []int64) error {
	ctx, cancel := dbCtx()
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for position, id := range ids {
		if _, err := tx.ExecContext(ctx, `UPDATE `+tableFormQuestions+` SET position = $1 WHERE id = $2 AND form_id = $3`,
			position, id, formID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *pgFormsStore) ChatRoles(formID int64) ([]string, error) {
	ctx, cancel := dbCtx()
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `SELECT role_id FROM `+tableFormChatStaff+` WHERE form_id = $1 ORDER BY added_at, role_id`, formID)
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

func (s *pgFormsStore) AddChatRole(formID int64, roleID string) error {
	ctx, cancel := dbCtx()
	defer cancel()
	_, err := s.db.ExecContext(ctx, `INSERT INTO `+tableFormChatStaff+` (form_id, role_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, formID, roleID)
	return err
}

func (s *pgFormsStore) RemoveChatRole(formID int64, roleID string) error {
	ctx, cancel := dbCtx()
	defer cancel()
	_, err := s.db.ExecContext(ctx, `DELETE FROM `+tableFormChatStaff+` WHERE form_id = $1 AND role_id = $2`, formID, roleID)
	return err
}

const entryColumns = `id, form_id, guild_id, user_id, answers, status, submitted_at, review_channel_id, review_message_id, decided_by, note, chat_channel_id`

func scanEntry(row interface{ Scan(...any) error }) (*appEntry, error) {
	var e appEntry
	var answers []byte
	err := row.Scan(&e.ID, &e.FormID, &e.GuildID, &e.UserID, &answers, &e.Status, &e.SubmittedAt, &e.ReviewChannelID,
		&e.ReviewMessageID, &e.DecidedBy, &e.Note, &e.ChatChannelID)
	if err != nil {
		return nil, err
	}
	e.Answers = string(answers)
	return &e, nil
}

func (s *pgFormsStore) Submit(e *appEntry) (int64, error) {
	ctx, cancel := dbCtx()
	defer cancel()
	answers := e.Answers
	if answers == "" {
		answers = "[]"
	}
	var id int64
	err := s.db.QueryRowContext(ctx, `INSERT INTO `+tableFormEntries+` (form_id, guild_id, user_id, answers) VALUES ($1, $2, $3, $4) RETURNING id`,
		e.FormID, e.GuildID, e.UserID, answers).Scan(&id)
	if isUniqueViolation(err) {
		return 0, errAlreadyExists
	}
	return id, err
}

func (s *pgFormsStore) GetEntry(guildID string, id int64) (*appEntry, error) {
	ctx, cancel := dbCtx()
	defer cancel()
	e, err := scanEntry(s.db.QueryRowContext(ctx, `SELECT `+entryColumns+` FROM `+tableFormEntries+` WHERE id = $1 AND guild_id = $2`, id, guildID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return e, err
}

func (s *pgFormsStore) ListEntries(guildID string, formID int64, status string) ([]appEntry, error) {
	ctx, cancel := dbCtx()
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `SELECT `+entryColumns+` FROM `+tableFormEntries+
		` WHERE guild_id = $1 AND ($2 = 0 OR form_id = $2) AND ($3 = '' OR status = $3) ORDER BY id`, guildID, formID, status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []appEntry{}
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

func (s *pgFormsStore) LatestEntry(formID int64, userID string) (*appEntry, error) {
	ctx, cancel := dbCtx()
	defer cancel()
	e, err := scanEntry(s.db.QueryRowContext(ctx, `SELECT `+entryColumns+` FROM `+tableFormEntries+
		` WHERE form_id = $1 AND user_id = $2 ORDER BY id DESC LIMIT 1`, formID, userID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return e, err
}

func (s *pgFormsStore) SetReviewMessage(entryID int64, channelID, messageID string) error {
	ctx, cancel := dbCtx()
	defer cancel()
	_, err := s.db.ExecContext(ctx, `UPDATE `+tableFormEntries+` SET review_channel_id = $2, review_message_id = $3 WHERE id = $1`,
		entryID, channelID, messageID)
	return err
}

func (s *pgFormsStore) SetChat(entryID int64, channelID string) error {
	ctx, cancel := dbCtx()
	defer cancel()
	_, err := s.db.ExecContext(ctx, `UPDATE `+tableFormEntries+` SET chat_channel_id = $2 WHERE id = $1`, entryID, channelID)
	return err
}

func (s *pgFormsStore) EarlierChats(formID int64, userID string) ([]string, error) {
	ctx, cancel := dbCtx()
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `SELECT chat_channel_id FROM `+tableFormEntries+
		` WHERE form_id = $1 AND user_id = $2 AND chat_channel_id <> '' ORDER BY id DESC`, formID, userID)
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

func (s *pgFormsStore) Decide(entryID int64, status, by, note string) (bool, error) {
	ctx, cancel := dbCtx()
	defer cancel()
	res, err := s.db.ExecContext(ctx, `UPDATE `+tableFormEntries+` SET status = $2, decided_by = $3, decided_at = NOW(), note = $4
		WHERE id = $1 AND status = 'pending'`, entryID, status, by, note)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (s *pgFormsStore) Votes(entryID int64) ([]appVote, error) {
	ctx, cancel := dbCtx()
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `SELECT user_id, vote FROM `+tableFormVotes+` WHERE entry_id = $1 ORDER BY voted_at, user_id`, entryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []appVote{}
	for rows.Next() {
		var v appVote
		if err := rows.Scan(&v.UserID, &v.Vote); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *pgFormsStore) SetVote(entryID int64, userID, vote string) error {
	ctx, cancel := dbCtx()
	defer cancel()
	if vote == "" {
		_, err := s.db.ExecContext(ctx, `DELETE FROM `+tableFormVotes+` WHERE entry_id = $1 AND user_id = $2`, entryID, userID)
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO `+tableFormVotes+` (entry_id, user_id, vote) VALUES ($1, $2, $3)
		ON CONFLICT (entry_id, user_id) DO UPDATE SET vote = EXCLUDED.vote, voted_at = NOW()`, entryID, userID, vote)
	return err
}
