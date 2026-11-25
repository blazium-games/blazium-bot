package main

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

// /remind: members ask the bot to remind them of something later.
//
//	/remind me in:2h about:"check the build" [here:True]   by DM, or in this channel with here:True
//	/remind list                                          your reminders in this server
//	/remind cancel number                                 drop one
//
// Reminders are kept in Postgres and checked every reminderTick. Each due reminder is
// claimed and removed in one statement (FOR UPDATE SKIP LOCKED), so two copies of the bot
// running during a deploy never send the same reminder twice. When a DM can't be sent,
// the reminder is posted in the channel it was made in instead.

const (
	reminderTick      = 20 * time.Second
	reminderMaxAhead  = 365 * 24 * time.Hour
	reminderMinAhead  = time.Minute
	reminderTextMax   = 1000
	maxRemindersOwned = 25
)

type reminder struct {
	ID        int64
	UserID    string
	GuildID   string
	ChannelID string // where it was made: posted there with here:True, or when DMs fail
	InChannel bool   // here:True
	Text      string
	DueAt     time.Time
	CreatedAt time.Time
}

type remindersStore interface {
	Add(r *reminder) (int64, error)
	// Of lists a member's reminders in a server, soonest first.
	Of(guildID, userID string) ([]reminder, error)
	// Cancel removes one of the member's reminders; false if they have no such reminder.
	Cancel(userID string, id int64) (bool, error)
	// TakeDue removes and returns up to limit reminders due by now.
	TakeDue(now time.Time, limit int) ([]reminder, error)
}

// reminders is nil when the database is unavailable.
var reminders remindersStore

// ============================================================================
// STORES
// ============================================================================

type memRemindersStore struct {
	mu   sync.Mutex
	next int64
	all  map[int64]reminder
}

func newMemRemindersStore() *memRemindersStore {
	return &memRemindersStore{all: map[int64]reminder{}}
}

func (m *memRemindersStore) Add(r *reminder) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.next++
	saved := *r
	saved.ID = m.next
	saved.CreatedAt = time.Now()
	m.all[saved.ID] = saved
	return saved.ID, nil
}

func (m *memRemindersStore) Of(guildID, userID string) ([]reminder, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []reminder{}
	for _, r := range m.all {
		if r.GuildID == guildID && r.UserID == userID {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].DueAt.Before(out[b].DueAt) })
	return out, nil
}

func (m *memRemindersStore) Cancel(userID string, id int64) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r, ok := m.all[id]; ok && r.UserID == userID {
		delete(m.all, id)
		return true, nil
	}
	return false, nil
}

func (m *memRemindersStore) TakeDue(now time.Time, limit int) ([]reminder, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []reminder{}
	for _, r := range m.all {
		if !r.DueAt.After(now) {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].DueAt.Before(out[b].DueAt) })
	if len(out) > limit {
		out = out[:limit]
	}
	for _, r := range out {
		delete(m.all, r.ID)
	}
	return out, nil
}

const tableReminders = botTablePrefix + "reminders"

type pgRemindersStore struct{ db *sql.DB }

func newPostgresRemindersStore(db *sql.DB) (*pgRemindersStore, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := ensureTables(ctx, db, []string{
		`CREATE TABLE IF NOT EXISTS ` + tableReminders + ` (
			id BIGSERIAL PRIMARY KEY,
			user_id TEXT NOT NULL,
			guild_id TEXT NOT NULL,
			channel_id TEXT NOT NULL,
			in_channel BOOLEAN NOT NULL DEFAULT FALSE,
			text TEXT NOT NULL,
			due_at TIMESTAMPTZ NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`CREATE INDEX IF NOT EXISTS ` + tableReminders + `_due_idx ON ` + tableReminders + ` (due_at)`,
		`CREATE INDEX IF NOT EXISTS ` + tableReminders + `_user_idx ON ` + tableReminders + ` (guild_id, user_id)`,
	})
	if err != nil {
		return nil, err
	}
	return &pgRemindersStore{db: db}, nil
}

const reminderColumns = `id, user_id, guild_id, channel_id, in_channel, text, due_at, created_at`

func (s *pgRemindersStore) query(q string, args ...any) ([]reminder, error) {
	ctx, cancel := dbCtx()
	defer cancel()
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []reminder{}
	for rows.Next() {
		var r reminder
		if err := rows.Scan(&r.ID, &r.UserID, &r.GuildID, &r.ChannelID, &r.InChannel, &r.Text, &r.DueAt, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *pgRemindersStore) Add(r *reminder) (int64, error) {
	ctx, cancel := dbCtx()
	defer cancel()
	var id int64
	err := s.db.QueryRowContext(ctx, `INSERT INTO `+tableReminders+` (user_id, guild_id, channel_id, in_channel, text, due_at)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`, r.UserID, r.GuildID, r.ChannelID, r.InChannel, r.Text, r.DueAt).Scan(&id)
	return id, err
}

func (s *pgRemindersStore) Of(guildID, userID string) ([]reminder, error) {
	return s.query(`SELECT `+reminderColumns+` FROM `+tableReminders+` WHERE guild_id = $1 AND user_id = $2 ORDER BY due_at`, guildID, userID)
}

func (s *pgRemindersStore) Cancel(userID string, id int64) (bool, error) {
	ctx, cancel := dbCtx()
	defer cancel()
	res, err := s.db.ExecContext(ctx, `DELETE FROM `+tableReminders+` WHERE id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (s *pgRemindersStore) TakeDue(now time.Time, limit int) ([]reminder, error) {
	return s.query(`DELETE FROM `+tableReminders+` WHERE id IN (
		SELECT id FROM `+tableReminders+` WHERE due_at <= $1 ORDER BY due_at LIMIT $2 FOR UPDATE SKIP LOCKED
	) RETURNING `+reminderColumns, now, limit)
}

// ============================================================================
// COMMAND
// ============================================================================

func remindCommand() *SlashCommandHandler {
	return &SlashCommandHandler{
		Name: "remind", Description: "Get reminded of something later", Handler: handleRemindCommand,
		Options: []*discordgo.ApplicationCommandOption{
			sub("me", "Set a reminder",
				strOpt("in", "When, like 30m, 2h, 1d or 1w2d", true, 30),
				strOpt("about", "What to remind you of", true, reminderTextMax),
				boolOpt("here", "Remind me in this channel instead of by DM"),
			),
			sub("list", "Your reminders in this server"),
			sub("cancel", "Drop one of your reminders", intOpt("number", "The reminder's number, from /remind list", true, 1, 1e15)),
		},
	}
}

func initReminders() {
	db := botDatabase()
	if db == nil {
		appLogger.Warn("Postgres is unavailable; reminders disabled")
		return
	}
	store, err := newPostgresRemindersStore(db)
	if err != nil {
		appLogger.Errorf("Reminders disabled: %v", err)
		return
	}
	reminders = store
	appLogger.Info("Reminders ready")
}

func registerReminders() {
	registerGuildCommand(remindCommand())
}

// planReminder checks a new reminder and works out when it's due.
func planReminder(when, text string, now time.Time, owned int) (time.Time, string, error) {
	d, err := parseDuration(when)
	if err != nil {
		return time.Time{}, "", err
	}
	if d < reminderMinAhead {
		return time.Time{}, "", userErrorf("A reminder must be at least a minute away.")
	}
	if d > reminderMaxAhead {
		return time.Time{}, "", userErrorf("Reminders can be up to a year away.")
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return time.Time{}, "", userErrorf("Say what to remind you of.")
	}
	if owned >= maxRemindersOwned {
		return time.Time{}, "", userErrorf("You already have %d reminders here. Cancel one with `/remind cancel` first.", owned)
	}
	return now.Add(d), truncateRunes(text, reminderTextMax), nil
}

func handleRemindCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if i.GuildID == "" {
		respondEphemeral(s, i, "This only works in a server.")
		return
	}
	if reminders == nil {
		respondEphemeral(s, i, "Reminders need the database, which isn't available right now.")
		return
	}
	sub, opts := commandOptions(i)
	msg, err := runRemind(i, sub, opts)
	if err != nil {
		msg = errorText("/remind "+sub, err)
	}
	respondEphemeral(s, i, truncateRunes(msg, 2000))
}

func runRemind(i *discordgo.InteractionCreate, sub string, opts cmdOpts) (string, error) {
	user := interactionUser(i)
	switch sub {
	case "me":
		mine, err := reminders.Of(i.GuildID, user.ID)
		if err != nil {
			return "", err
		}
		when, _ := optString(opts, "in")
		about, _ := optString(opts, "about")
		here, _ := optBool(opts, "here")
		due, text, err := planReminder(when, about, time.Now(), len(mine))
		if err != nil {
			return "", err
		}
		id, err := reminders.Add(&reminder{UserID: user.ID, GuildID: i.GuildID, ChannelID: i.ChannelID, InChannel: here, Text: text, DueAt: due})
		if err != nil {
			return "", err
		}
		where := "by DM"
		if here {
			where = "in this channel"
		}
		return fmt.Sprintf("⏰ I'll remind you <t:%d:R> (<t:%d:f>) %s. It's reminder #%d.", due.Unix(), due.Unix(), where, id), nil
	case "list":
		mine, err := reminders.Of(i.GuildID, user.ID)
		if err != nil {
			return "", err
		}
		if len(mine) == 0 {
			return "You have no reminders here. Set one with `/remind me`.", nil
		}
		var b strings.Builder
		b.WriteString("**Your reminders**\n")
		for _, r := range mine {
			fmt.Fprintf(&b, "`#%d` <t:%d:R>: %s\n", r.ID, r.DueAt.Unix(), truncateRunes(strings.ReplaceAll(r.Text, "\n", " "), 80))
		}
		return b.String(), nil
	case "cancel":
		id, _ := optInt(opts, "number")
		ok, err := reminders.Cancel(user.ID, id)
		if err != nil {
			return "", err
		}
		if !ok {
			return "", userErrorf("You have no reminder #%d.", id)
		}
		return fmt.Sprintf("Cancelled reminder #%d.", id), nil
	}
	return "", userErrorf("Unknown subcommand.")
}

// ============================================================================
// DELIVERY
// ============================================================================

var (
	restSessionMu sync.RWMutex
	restSession   *discordgo.Session
	remindersOnce sync.Once
)

// rememberSession keeps a connected session for background work such as reminders, and
// starts the reminder loop the first time any shard is ready.
func rememberSession(s *discordgo.Session, _ *discordgo.Ready) {
	restSessionMu.Lock()
	restSession = s
	restSessionMu.Unlock()
	remindersOnce.Do(func() { go reminderLoop() })
}

func currentSession() *discordgo.Session {
	restSessionMu.RLock()
	defer restSessionMu.RUnlock()
	return restSession
}

func reminderLoop() {
	ticker := time.NewTicker(reminderTick)
	defer ticker.Stop()
	for range ticker.C {
		if CommandManager != nil && CommandManager.getShuttingDown() {
			return
		}
		if reminders == nil {
			continue
		}
		s := currentSession()
		if s == nil {
			continue
		}
		due, err := reminders.TakeDue(time.Now(), 50)
		if err != nil {
			appLogger.Warnf("Could not read due reminders: %v", err)
			continue
		}
		for _, r := range due {
			deliverReminder(s, r)
		}
	}
}

func reminderEmbed(r reminder) *discordgo.MessageEmbed {
	return &discordgo.MessageEmbed{
		Title:       "⏰ Reminder",
		Description: r.Text,
		Color:       colorNotice,
		Footer:      &discordgo.MessageEmbedFooter{Text: "You asked for this"},
		Timestamp:   r.CreatedAt.Format(time.RFC3339),
	}
}

func deliverReminder(s *discordgo.Session, r reminder) {
	embed := reminderEmbed(r)
	if !r.InChannel {
		if dm, err := s.UserChannelCreate(r.UserID); err == nil {
			if _, err := s.ChannelMessageSendEmbed(dm.ID, embed); err == nil {
				return
			}
		}
	}
	_, err := s.ChannelMessageSendComplex(r.ChannelID, &discordgo.MessageSend{
		Content:         "<@" + r.UserID + ">",
		Embeds:          []*discordgo.MessageEmbed{embed},
		AllowedMentions: &discordgo.MessageAllowedMentions{Users: []string{r.UserID}},
	})
	if err != nil {
		appLogger.Warnf("Could not deliver reminder %d to %s: %v", r.ID, r.UserID, err)
	}
}
