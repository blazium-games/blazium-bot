package main

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

const (
	tableForms         = botTablePrefix + "forms"
	tableFormQuestions = botTablePrefix + "form_questions"
	tableSubmissions   = botTablePrefix + "form_submissions"
	tableFormVotes     = botTablePrefix + "form_votes"
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
			channel_id TEXT NOT NULL,
			button_label TEXT NOT NULL DEFAULT '',
			emoji TEXT NOT NULL DEFAULT '',
			open BOOLEAN NOT NULL DEFAULT TRUE,
			header TEXT NOT NULL DEFAULT '',
			max_per_member INTEGER NOT NULL DEFAULT 1,
			dm_receipt BOOLEAN NOT NULL DEFAULT TRUE,
			review BOOLEAN NOT NULL DEFAULT FALSE,
			accept_role_id TEXT NOT NULL DEFAULT '',
			reviewer_role_id TEXT NOT NULL DEFAULT '',
			dm_accept BOOLEAN NOT NULL DEFAULT TRUE,
			dm_decline BOOLEAN NOT NULL DEFAULT TRUE,
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
		`CREATE TABLE IF NOT EXISTS ` + tableSubmissions + ` (
			id BIGSERIAL PRIMARY KEY,
			form_id BIGINT NOT NULL REFERENCES ` + tableForms + `(id) ON DELETE CASCADE,
			guild_id TEXT NOT NULL,
			user_id TEXT NOT NULL,
			answers JSONB NOT NULL DEFAULT '[]',
			status TEXT NOT NULL,
			submitted_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			channel_id TEXT NOT NULL DEFAULT '',
			message_id TEXT NOT NULL DEFAULT '',
			decided_by TEXT NOT NULL DEFAULT '',
			decided_at TIMESTAMPTZ,
			note TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS ` + tableSubmissions + `_form_idx ON ` + tableSubmissions + ` (form_id, user_id)`,
		`CREATE INDEX IF NOT EXISTS ` + tableSubmissions + `_guild_idx ON ` + tableSubmissions + ` (guild_id, status)`,
		`CREATE TABLE IF NOT EXISTS ` + tableFormVotes + ` (
			submission_id BIGINT NOT NULL REFERENCES ` + tableSubmissions + `(id) ON DELETE CASCADE,
			user_id TEXT NOT NULL,
			vote TEXT NOT NULL,
			voted_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			PRIMARY KEY (submission_id, user_id)
		)`,
	})
	if err != nil {
		return nil, err
	}
	return &pgFormsStore{db: db}, nil
}

const formColumns = `id, guild_id, name, intro, channel_id, button_label, emoji, open, header, max_per_member, dm_receipt, review, accept_role_id, reviewer_role_id, dm_accept, dm_decline, created_by, created_at`

func scanForm(row interface{ Scan(...any) error }) (*appForm, error) {
	var f appForm
	err := row.Scan(&f.ID, &f.GuildID, &f.Name, &f.Intro, &f.ChannelID, &f.ButtonLabel, &f.Emoji, &f.Open, &f.Header, &f.MaxPerMember,
		&f.DMReceipt, &f.Review, &f.AcceptRoleID, &f.ReviewerRoleID, &f.DMAccept, &f.DMDecline, &f.CreatedBy, &f.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &f, nil
}

func (s *pgFormsStore) CreateForm(f *appForm) (int64, error) {
	ctx, cancel := dbCtx()
	defer cancel()
	var id int64
	err := s.db.QueryRowContext(ctx, `INSERT INTO `+tableForms+` (guild_id, name, intro, channel_id, button_label, emoji, open, header,
		max_per_member, dm_receipt, review, accept_role_id, reviewer_role_id, dm_accept, dm_decline, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16) RETURNING id`,
		f.GuildID, f.Name, f.Intro, f.ChannelID, f.ButtonLabel, f.Emoji, f.Open, f.Header, f.MaxPerMember, f.DMReceipt, f.Review,
		f.AcceptRoleID, f.ReviewerRoleID, f.DMAccept, f.DMDecline, f.CreatedBy).Scan(&id)
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
	_, err := s.db.ExecContext(ctx, `UPDATE `+tableForms+` SET name = $2, intro = $3, channel_id = $4, button_label = $5, emoji = $6,
		open = $7, header = $8, max_per_member = $9, dm_receipt = $10, review = $11, accept_role_id = $12, reviewer_role_id = $13,
		dm_accept = $14, dm_decline = $15 WHERE id = $1`,
		f.ID, f.Name, f.Intro, f.ChannelID, f.ButtonLabel, f.Emoji, f.Open, f.Header, f.MaxPerMember, f.DMReceipt, f.Review,
		f.AcceptRoleID, f.ReviewerRoleID, f.DMAccept, f.DMDecline)
	return err
}

func (s *pgFormsStore) DeleteForm(id int64) error {
	ctx, cancel := dbCtx()
	defer cancel()
	// Questions, submissions and votes go with it (ON DELETE CASCADE).
	_, err := s.db.ExecContext(ctx, `DELETE FROM `+tableForms+` WHERE id = $1`, id)
	return err
}

func (s *pgFormsStore) Questions(formID int64) ([]formQuestion, error) {
	return pgQuestions(s.db, tableFormQuestions, "form_id", formID)
}

func (s *pgFormsStore) AddQuestion(formID int64, q formQuestion) error {
	return pgAddQuestion(s.db, tableFormQuestions, "form_id", formID, q)
}

func (s *pgFormsStore) RemoveQuestion(formID, questionID int64) error {
	return pgRemoveQuestion(s.db, tableFormQuestions, "form_id", formID, questionID)
}

func (s *pgFormsStore) ReorderQuestions(formID int64, ids []int64) error {
	return pgReorderQuestions(s.db, tableFormQuestions, "form_id", formID, ids)
}

const submissionColumns = `id, form_id, guild_id, user_id, answers, status, submitted_at, channel_id, message_id, decided_by, note`

func scanSubmission(row interface{ Scan(...any) error }) (*submission, error) {
	var sub submission
	var answers []byte
	err := row.Scan(&sub.ID, &sub.FormID, &sub.GuildID, &sub.UserID, &answers, &sub.Status, &sub.SubmittedAt, &sub.ChannelID,
		&sub.MessageID, &sub.DecidedBy, &sub.Note)
	if err != nil {
		return nil, err
	}
	sub.Answers = string(answers)
	return &sub, nil
}

func (s *pgFormsStore) Submit(sub *submission) (int64, error) {
	ctx, cancel := dbCtx()
	defer cancel()
	answers, status := sub.Answers, sub.Status
	if answers == "" {
		answers = "[]"
	}
	if status == "" {
		status = subReceived
	}
	var id int64
	err := s.db.QueryRowContext(ctx, `INSERT INTO `+tableSubmissions+` (form_id, guild_id, user_id, answers, status) VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		sub.FormID, sub.GuildID, sub.UserID, answers, status).Scan(&id)
	return id, err
}

func (s *pgFormsStore) GetSubmission(guildID string, id int64) (*submission, error) {
	ctx, cancel := dbCtx()
	defer cancel()
	sub, err := scanSubmission(s.db.QueryRowContext(ctx, `SELECT `+submissionColumns+` FROM `+tableSubmissions+` WHERE id = $1 AND guild_id = $2`, id, guildID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return sub, err
}

func (s *pgFormsStore) querySubmissions(query string, args ...any) ([]submission, error) {
	ctx, cancel := dbCtx()
	defer cancel()
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []submission{}
	for rows.Next() {
		sub, err := scanSubmission(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *sub)
	}
	return out, rows.Err()
}

func (s *pgFormsStore) ListSubmissions(guildID string, formID int64, status string) ([]submission, error) {
	return s.querySubmissions(`SELECT `+submissionColumns+` FROM `+tableSubmissions+
		` WHERE guild_id = $1 AND ($2 = 0 OR form_id = $2) AND ($3 = '' OR status = $3) ORDER BY id DESC`, guildID, formID, status)
}

func (s *pgFormsStore) SubmissionsOf(formID int64, userID string) ([]submission, error) {
	return s.querySubmissions(`SELECT `+submissionColumns+` FROM `+tableSubmissions+` WHERE form_id = $1 AND user_id = $2 ORDER BY id DESC`, formID, userID)
}

func (s *pgFormsStore) SetSubmissionMessage(id int64, channelID, messageID string) error {
	ctx, cancel := dbCtx()
	defer cancel()
	_, err := s.db.ExecContext(ctx, `UPDATE `+tableSubmissions+` SET channel_id = $2, message_id = $3 WHERE id = $1`, id, channelID, messageID)
	return err
}

func (s *pgFormsStore) Decide(id int64, status, by, note string) (bool, error) {
	ctx, cancel := dbCtx()
	defer cancel()
	res, err := s.db.ExecContext(ctx, `UPDATE `+tableSubmissions+` SET status = $2, decided_by = $3, decided_at = NOW(), note = $4
		WHERE id = $1 AND status = 'pending'`, id, status, by, note)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (s *pgFormsStore) Votes(subID int64) ([]appVote, error) {
	ctx, cancel := dbCtx()
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `SELECT user_id, vote FROM `+tableFormVotes+` WHERE submission_id = $1 ORDER BY voted_at, user_id`, subID)
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

func (s *pgFormsStore) SetVote(subID int64, userID, vote string) error {
	ctx, cancel := dbCtx()
	defer cancel()
	if vote == "" {
		_, err := s.db.ExecContext(ctx, `DELETE FROM `+tableFormVotes+` WHERE submission_id = $1 AND user_id = $2`, subID, userID)
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO `+tableFormVotes+` (submission_id, user_id, vote) VALUES ($1, $2, $3)
		ON CONFLICT (submission_id, user_id) DO UPDATE SET vote = EXCLUDED.vote, voted_at = NOW()`, subID, userID, vote)
	return err
}
