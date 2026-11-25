package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

const (
	tableGuildSettings = botTablePrefix + "guild_settings"
	tableModCases      = botTablePrefix + "mod_cases"
)

type pgServerStore struct {
	db *sql.DB
}

func newPostgresServerStore(db *sql.DB) (*pgServerStore, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := ensureTables(ctx, db, []string{
		`CREATE TABLE IF NOT EXISTS ` + tableGuildSettings + ` (
			guild_id TEXT PRIMARY KEY,
			mod_log_channel TEXT NOT NULL DEFAULT '',
			member_log_channel TEXT NOT NULL DEFAULT '',
			message_log_channel TEXT NOT NULL DEFAULT '',
			welcome_channel TEXT NOT NULL DEFAULT '',
			welcome_message TEXT NOT NULL DEFAULT '',
			goodbye_channel TEXT NOT NULL DEFAULT '',
			goodbye_message TEXT NOT NULL DEFAULT '',
			auto_roles JSONB NOT NULL DEFAULT '[]',
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`CREATE TABLE IF NOT EXISTS ` + tableModCases + ` (
			id BIGSERIAL PRIMARY KEY,
			guild_id TEXT NOT NULL,
			number BIGINT NOT NULL,
			action TEXT NOT NULL,
			user_id TEXT NOT NULL,
			moderator_id TEXT NOT NULL,
			reason TEXT NOT NULL DEFAULT '',
			duration_seconds BIGINT NOT NULL DEFAULT 0,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			UNIQUE (guild_id, number)
		)`,
		`CREATE INDEX IF NOT EXISTS ` + tableModCases + `_user_idx ON ` + tableModCases + ` (guild_id, user_id)`,
	})
	if err != nil {
		return nil, err
	}
	return &pgServerStore{db: db}, nil
}

func (s *pgServerStore) Settings(guildID string) (*guildSettings, error) {
	ctx, cancel := dbCtx()
	defer cancel()
	g := guildSettings{GuildID: guildID}
	var roles []byte
	err := s.db.QueryRowContext(ctx, `SELECT mod_log_channel, member_log_channel, message_log_channel, welcome_channel,
		welcome_message, goodbye_channel, goodbye_message, auto_roles FROM `+tableGuildSettings+` WHERE guild_id = $1`, guildID).
		Scan(&g.ModLogChannel, &g.MemberLogChannel, &g.MessageLogChannel, &g.WelcomeChannel, &g.WelcomeMessage,
			&g.GoodbyeChannel, &g.GoodbyeMessage, &roles)
	if errors.Is(err, sql.ErrNoRows) {
		return &g, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(roles, &g.AutoRoles); err != nil {
		return nil, err
	}
	return &g, nil
}

func (s *pgServerStore) SaveSettings(g *guildSettings) error {
	roles := g.AutoRoles
	if roles == nil {
		roles = []string{}
	}
	rolesJSON, err := json.Marshal(roles)
	if err != nil {
		return err
	}
	ctx, cancel := dbCtx()
	defer cancel()
	_, err = s.db.ExecContext(ctx, `INSERT INTO `+tableGuildSettings+` (guild_id, mod_log_channel, member_log_channel,
		message_log_channel, welcome_channel, welcome_message, goodbye_channel, goodbye_message, auto_roles, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, NOW())
		ON CONFLICT (guild_id) DO UPDATE SET mod_log_channel = $2, member_log_channel = $3, message_log_channel = $4,
		welcome_channel = $5, welcome_message = $6, goodbye_channel = $7, goodbye_message = $8, auto_roles = $9, updated_at = NOW()`,
		g.GuildID, g.ModLogChannel, g.MemberLogChannel, g.MessageLogChannel, g.WelcomeChannel, g.WelcomeMessage,
		g.GoodbyeChannel, g.GoodbyeMessage, string(rolesJSON))
	return err
}

const modCaseColumns = `id, guild_id, number, action, user_id, moderator_id, reason, duration_seconds, created_at`

func scanModCase(row interface{ Scan(...any) error }) (*modCase, error) {
	var c modCase
	var seconds int64
	if err := row.Scan(&c.ID, &c.GuildID, &c.Number, &c.Action, &c.UserID, &c.ModeratorID, &c.Reason, &seconds, &c.CreatedAt); err != nil {
		return nil, err
	}
	c.Duration = time.Duration(seconds) * time.Second
	return &c, nil
}

func (s *pgServerStore) AddCase(c *modCase) (int64, error) {
	// Two cases made at the same moment can pick the same number; the unique
	// (guild_id, number) index refuses the second, which tries again.
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		ctx, cancel := dbCtx()
		var number int64
		lastErr = s.db.QueryRowContext(ctx, `INSERT INTO `+tableModCases+` (guild_id, number, action, user_id, moderator_id, reason, duration_seconds)
			VALUES ($1, (SELECT COALESCE(MAX(number), 0) + 1 FROM `+tableModCases+` WHERE guild_id = $1), $2, $3, $4, $5, $6)
			RETURNING number`, c.GuildID, c.Action, c.UserID, c.ModeratorID, c.Reason, int64(c.Duration/time.Second)).Scan(&number)
		cancel()
		if lastErr == nil {
			return number, nil
		}
		if !isUniqueViolation(lastErr) {
			return 0, lastErr
		}
	}
	return 0, lastErr
}

func (s *pgServerStore) Case(guildID string, number int64) (*modCase, error) {
	ctx, cancel := dbCtx()
	defer cancel()
	c, err := scanModCase(s.db.QueryRowContext(ctx, `SELECT `+modCaseColumns+` FROM `+tableModCases+
		` WHERE guild_id = $1 AND number = $2`, guildID, number))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return c, err
}

func (s *pgServerStore) Cases(guildID, userID string, limit int) ([]modCase, error) {
	if limit <= 0 {
		limit = 1000
	}
	ctx, cancel := dbCtx()
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `SELECT `+modCaseColumns+` FROM `+tableModCases+
		` WHERE guild_id = $1 AND ($2 = '' OR user_id = $2) ORDER BY number DESC LIMIT $3`, guildID, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []modCase{}
	for rows.Next() {
		c, err := scanModCase(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

func (s *pgServerStore) changed(query string, args ...any) (bool, error) {
	ctx, cancel := dbCtx()
	defer cancel()
	res, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (s *pgServerStore) UpdateCaseReason(guildID string, number int64, reason string) (bool, error) {
	return s.changed(`UPDATE `+tableModCases+` SET reason = $3 WHERE guild_id = $1 AND number = $2`, guildID, number, reason)
}

func (s *pgServerStore) DeleteCase(guildID string, number int64) (bool, error) {
	return s.changed(`DELETE FROM `+tableModCases+` WHERE guild_id = $1 AND number = $2`, guildID, number)
}
