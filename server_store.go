package main

import (
	"sort"
	"sync"
	"time"
)

// Where per-server settings (log channels, welcome and goodbye messages, auto-roles) and
// moderation cases are kept. Postgres in production (server_db.go); the in-memory store
// below backs the tests.

type guildSettings struct {
	GuildID           string
	ModLogChannel     string   // warns, timeouts, kicks, bans and purges
	MemberLogChannel  string   // joins and leaves
	MessageLogChannel string   // deleted and edited messages
	WelcomeChannel    string   // "" is off
	WelcomeMessage    string   // with {user}, {username}, {server}, {count}
	GoodbyeChannel    string   // "" is off
	GoodbyeMessage    string   // same placeholders
	AutoRoles         []string // given to every member who joins (bots are skipped)
}

const (
	caseWarn    = "warn"
	caseTimeout = "timeout"
	caseUntime  = "untimeout"
	caseKick    = "kick"
	caseBan     = "ban"
	caseUnban   = "unban"
	caseNote    = "note"
)

type modCase struct {
	ID          int64
	GuildID     string
	Number      int64 // counts up per server: case #12
	Action      string
	UserID      string
	ModeratorID string
	Reason      string
	Duration    time.Duration // timeouts and temporary bans; 0 otherwise
	CreatedAt   time.Time
}

type serverStore interface {
	// Settings returns the server's settings; a server with none yet gets empty ones.
	Settings(guildID string) (*guildSettings, error)
	SaveSettings(g *guildSettings) error

	// AddCase saves a case and gives it the server's next number.
	AddCase(c *modCase) (number int64, err error)
	// Case returns nil, nil when there is no such case in that server.
	Case(guildID string, number int64) (*modCase, error)
	// Cases lists a server's cases, newest first. userID "" is every member.
	Cases(guildID, userID string, limit int) ([]modCase, error)
	// UpdateCaseReason changes a case's reason; false if there is no such case.
	UpdateCaseReason(guildID string, number int64, reason string) (bool, error)
	// DeleteCase removes a case (a warning given by mistake); false if there is no such case.
	DeleteCase(guildID string, number int64) (bool, error)
}

// servers is nil when the database is unavailable; settings and moderation history are then off.
var servers serverStore

type memServerStore struct {
	mu       sync.Mutex
	next     int64
	settings map[string]*guildSettings
	cases    map[int64]*modCase
}

func newMemServerStore() *memServerStore {
	return &memServerStore{settings: map[string]*guildSettings{}, cases: map[int64]*modCase{}}
}

func copySettings(g *guildSettings) *guildSettings {
	c := *g
	c.AutoRoles = append([]string(nil), g.AutoRoles...)
	return &c
}

func (m *memServerStore) Settings(guildID string) (*guildSettings, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if g, ok := m.settings[guildID]; ok {
		return copySettings(g), nil
	}
	return &guildSettings{GuildID: guildID}, nil
}

func (m *memServerStore) SaveSettings(g *guildSettings) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.settings[g.GuildID] = copySettings(g)
	return nil
}

func (m *memServerStore) AddCase(c *modCase) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var top int64
	for _, x := range m.cases {
		if x.GuildID == c.GuildID && x.Number > top {
			top = x.Number
		}
	}
	m.next++
	saved := *c
	saved.ID = m.next
	saved.Number = top + 1
	if saved.CreatedAt.IsZero() {
		saved.CreatedAt = time.Now()
	}
	m.cases[saved.ID] = &saved
	return saved.Number, nil
}

func (m *memServerStore) find(guildID string, number int64) *modCase {
	for _, c := range m.cases {
		if c.GuildID == guildID && c.Number == number {
			return c
		}
	}
	return nil
}

func (m *memServerStore) Case(guildID string, number int64) (*modCase, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c := m.find(guildID, number); c != nil {
		copied := *c
		return &copied, nil
	}
	return nil, nil
}

func (m *memServerStore) Cases(guildID, userID string, limit int) ([]modCase, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []modCase{}
	for _, c := range m.cases {
		if c.GuildID == guildID && (userID == "" || c.UserID == userID) {
			out = append(out, *c)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Number > out[b].Number })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *memServerStore) UpdateCaseReason(guildID string, number int64, reason string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := m.find(guildID, number)
	if c == nil {
		return false, nil
	}
	c.Reason = reason
	return true, nil
}

func (m *memServerStore) DeleteCase(guildID string, number int64) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := m.find(guildID, number)
	if c == nil {
		return false, nil
	}
	delete(m.cases, c.ID)
	return true, nil
}

// ============================================================================
// SETTINGS CACHE
// ============================================================================

// Member and message events read a server's settings on every event, so they are kept
// in memory for a short while. A save on this replica updates the cache at once; another
// replica picks the change up when its copy runs out.

const settingsCacheTTL = time.Minute

type cachedSettings struct {
	g       *guildSettings
	expires time.Time
}

var (
	settingsCacheMu sync.Mutex
	settingsCache   = map[string]cachedSettings{}
)

// serverSettings is the server's settings, or nil when they can't be read (no database).
func serverSettings(guildID string) *guildSettings {
	if servers == nil || guildID == "" {
		return nil
	}
	settingsCacheMu.Lock()
	c, ok := settingsCache[guildID]
	settingsCacheMu.Unlock()
	if ok && time.Now().Before(c.expires) {
		return c.g
	}
	g, err := servers.Settings(guildID)
	if err != nil {
		appLogger.Warnf("Could not read settings for server %s: %v", guildID, err)
		if ok {
			return c.g // a stale copy beats nothing
		}
		return nil
	}
	settingsCacheMu.Lock()
	settingsCache[guildID] = cachedSettings{g: g, expires: time.Now().Add(settingsCacheTTL)}
	settingsCacheMu.Unlock()
	return g
}

// saveServerSettings saves g and refreshes the cache.
func saveServerSettings(g *guildSettings) error {
	if err := servers.SaveSettings(g); err != nil {
		return err
	}
	settingsCacheMu.Lock()
	settingsCache[g.GuildID] = cachedSettings{g: copySettings(g), expires: time.Now().Add(settingsCacheTTL)}
	settingsCacheMu.Unlock()
	return nil
}
