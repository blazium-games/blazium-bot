package main

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

const (
	tableSignupPanels     = botTablePrefix + "signup_panels"
	tableSignupPanelRoles = botTablePrefix + "signup_panel_roles"
)

type pgSignupStore struct {
	db *sql.DB
}

func newPostgresSignupStore(db *sql.DB) (*pgSignupStore, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := ensureTables(ctx, db, []string{
		`CREATE TABLE IF NOT EXISTS ` + tableSignupPanels + ` (
			id BIGSERIAL PRIMARY KEY,
			guild_id TEXT NOT NULL,
			channel_id TEXT NOT NULL,
			message_id TEXT NOT NULL DEFAULT '',
			title TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			mode TEXT NOT NULL DEFAULT 'toggle',
			created_by TEXT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`CREATE INDEX IF NOT EXISTS ` + tableSignupPanels + `_guild_idx ON ` + tableSignupPanels + ` (guild_id)`,
		`CREATE TABLE IF NOT EXISTS ` + tableSignupPanelRoles + ` (
			panel_id BIGINT NOT NULL REFERENCES ` + tableSignupPanels + `(id) ON DELETE CASCADE,
			role_id TEXT NOT NULL,
			label TEXT NOT NULL,
			emoji TEXT NOT NULL DEFAULT '',
			style INTEGER NOT NULL DEFAULT 2,
			position INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (panel_id, role_id)
		)`,
		`CREATE INDEX IF NOT EXISTS ` + tableSignupPanelRoles + `_role_idx ON ` + tableSignupPanelRoles + ` (role_id)`,
	})
	if err != nil {
		return nil, err
	}
	return &pgSignupStore{db: db}, nil
}

func dbCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}

const signupPanelColumns = `id, guild_id, channel_id, message_id, title, description, mode, created_by, created_at`

func scanSignupPanel(row interface{ Scan(...any) error }, extra ...any) (*signupPanel, error) {
	var p signupPanel
	dest := append([]any{&p.ID, &p.GuildID, &p.ChannelID, &p.MessageID, &p.Title, &p.Description, &p.Mode, &p.CreatedBy, &p.CreatedAt}, extra...)
	if err := row.Scan(dest...); err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *pgSignupStore) CreatePanel(p *signupPanel) (int64, error) {
	ctx, cancel := dbCtx()
	defer cancel()
	var id int64
	err := s.db.QueryRowContext(ctx, `INSERT INTO `+tableSignupPanels+
		` (guild_id, channel_id, message_id, title, description, mode, created_by) VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`,
		p.GuildID, p.ChannelID, p.MessageID, p.Title, p.Description, p.Mode, p.CreatedBy).Scan(&id)
	return id, err
}

func (s *pgSignupStore) GetPanel(guildID string, id int64) (*signupPanel, error) {
	ctx, cancel := dbCtx()
	defer cancel()
	p, err := scanSignupPanel(s.db.QueryRowContext(ctx,
		`SELECT `+signupPanelColumns+` FROM `+tableSignupPanels+` WHERE id = $1 AND guild_id = $2`, id, guildID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return p, err
}

func (s *pgSignupStore) ListPanels(guildID string) ([]signupPanel, error) {
	ctx, cancel := dbCtx()
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `SELECT `+signupPanelColumns+
		`, (SELECT COUNT(*) FROM `+tableSignupPanelRoles+` r WHERE r.panel_id = p.id) FROM `+tableSignupPanels+
		` p WHERE guild_id = $1 ORDER BY id`, guildID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []signupPanel{}
	for rows.Next() {
		var count int
		p, err := scanSignupPanel(rows, &count)
		if err != nil {
			return nil, err
		}
		p.RoleCount = count
		out = append(out, *p)
	}
	return out, rows.Err()
}

func (s *pgSignupStore) UpdatePanel(p *signupPanel) error {
	ctx, cancel := dbCtx()
	defer cancel()
	_, err := s.db.ExecContext(ctx, `UPDATE `+tableSignupPanels+
		` SET channel_id = $2, message_id = $3, title = $4, description = $5, mode = $6 WHERE id = $1`,
		p.ID, p.ChannelID, p.MessageID, p.Title, p.Description, p.Mode)
	return err
}

func (s *pgSignupStore) DeletePanel(id int64) error {
	ctx, cancel := dbCtx()
	defer cancel()
	_, err := s.db.ExecContext(ctx, `DELETE FROM `+tableSignupPanels+` WHERE id = $1`, id)
	return err
}

func (s *pgSignupStore) PanelRoles(panelID int64) ([]signupPanelRole, error) {
	ctx, cancel := dbCtx()
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `SELECT panel_id, role_id, label, emoji, style, position FROM `+
		tableSignupPanelRoles+` WHERE panel_id = $1 ORDER BY position, role_id`, panelID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []signupPanelRole{}
	for rows.Next() {
		var r signupPanelRole
		if err := rows.Scan(&r.PanelID, &r.RoleID, &r.Label, &r.Emoji, &r.Style, &r.Position); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *pgSignupStore) AddPanelRole(r *signupPanelRole) error {
	ctx, cancel := dbCtx()
	defer cancel()
	_, err := s.db.ExecContext(ctx, `INSERT INTO `+tableSignupPanelRoles+
		` (panel_id, role_id, label, emoji, style, position) VALUES ($1, $2, $3, $4, $5, $6)`,
		r.PanelID, r.RoleID, r.Label, r.Emoji, r.Style, r.Position)
	if isUniqueViolation(err) {
		return errAlreadyExists
	}
	return err
}

func (s *pgSignupStore) RemovePanelRole(panelID int64, roleID string) error {
	ctx, cancel := dbCtx()
	defer cancel()
	_, err := s.db.ExecContext(ctx, `DELETE FROM `+tableSignupPanelRoles+` WHERE panel_id = $1 AND role_id = $2`, panelID, roleID)
	return err
}

func (s *pgSignupStore) RemoveRoleEverywhere(guildID, roleID string) ([]int64, error) {
	ctx, cancel := dbCtx()
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `DELETE FROM `+tableSignupPanelRoles+` r USING `+tableSignupPanels+
		` p WHERE r.panel_id = p.id AND p.guild_id = $1 AND r.role_id = $2 RETURNING r.panel_id`, guildID, roleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
