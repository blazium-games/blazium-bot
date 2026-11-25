package main

import (
	"sort"
	"sync"
	"time"
)

// Where sign-up panels are kept. Postgres in production (signup_panels_db.go); the
// in-memory store below backs the tests.

type signupPanel struct {
	ID          int64
	GuildID     string
	ChannelID   string
	MessageID   string // "" until posted
	Title       string
	Description string
	Mode        string // signupModeToggle, signupModeAddOnly or signupModeSingle
	CreatedBy   string
	CreatedAt   time.Time
	RoleCount   int // filled by ListPanels only
}

type signupPanelRole struct {
	PanelID  int64
	RoleID   string
	Label    string
	Emoji    string // "" for none; a unicode emoji or <:name:id>
	Style    int    // a discordgo.ButtonStyle
	Position int
}

type signupStore interface {
	CreatePanel(p *signupPanel) (int64, error)
	// GetPanel returns nil, nil when the panel doesn't exist in that server.
	GetPanel(guildID string, id int64) (*signupPanel, error)
	ListPanels(guildID string) ([]signupPanel, error)
	// UpdatePanel saves the channel, message, title, description and mode.
	UpdatePanel(p *signupPanel) error
	DeletePanel(id int64) error
	PanelRoles(panelID int64) ([]signupPanelRole, error)
	AddPanelRole(r *signupPanelRole) error
	RemovePanelRole(panelID int64, roleID string) error
	// RemoveRoleEverywhere drops a deleted role from every panel in a server and returns the panels it was on.
	RemoveRoleEverywhere(guildID, roleID string) ([]int64, error)
}

// signupPanels is nil when the database is unavailable; /panel then says so.
var signupPanels signupStore

type memSignupStore struct {
	mu     sync.Mutex
	nextID int64
	panels map[int64]*signupPanel
	roles  map[int64][]signupPanelRole
}

func newMemSignupStore() *memSignupStore {
	return &memSignupStore{panels: map[int64]*signupPanel{}, roles: map[int64][]signupPanelRole{}}
}

func (m *memSignupStore) CreatePanel(p *signupPanel) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	cp := *p
	cp.ID = m.nextID
	m.panels[cp.ID] = &cp
	return cp.ID, nil
}

func (m *memSignupStore) GetPanel(guildID string, id int64) (*signupPanel, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.panels[id]
	if !ok || p.GuildID != guildID {
		return nil, nil
	}
	cp := *p
	return &cp, nil
}

func (m *memSignupStore) ListPanels(guildID string) ([]signupPanel, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []signupPanel{}
	for _, p := range m.panels {
		if p.GuildID == guildID {
			cp := *p
			cp.RoleCount = len(m.roles[p.ID])
			out = append(out, cp)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].ID < out[b].ID })
	return out, nil
}

func (m *memSignupStore) UpdatePanel(p *signupPanel) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.panels[p.ID]
	if !ok {
		return errNotFound
	}
	cur.ChannelID, cur.MessageID, cur.Title, cur.Description, cur.Mode = p.ChannelID, p.MessageID, p.Title, p.Description, p.Mode
	return nil
}

func (m *memSignupStore) DeletePanel(id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.panels, id)
	delete(m.roles, id)
	return nil
}

func (m *memSignupStore) PanelRoles(panelID int64) ([]signupPanelRole, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := append([]signupPanelRole(nil), m.roles[panelID]...)
	sort.SliceStable(out, func(a, b int) bool { return out[a].Position < out[b].Position })
	return out, nil
}

func (m *memSignupStore) AddPanelRole(r *signupPanelRole) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, existing := range m.roles[r.PanelID] {
		if existing.RoleID == r.RoleID {
			return errAlreadyExists
		}
	}
	m.roles[r.PanelID] = append(m.roles[r.PanelID], *r)
	return nil
}

func (m *memSignupStore) RemovePanelRole(panelID int64, roleID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	kept := m.roles[panelID][:0]
	for _, r := range m.roles[panelID] {
		if r.RoleID != roleID {
			kept = append(kept, r)
		}
	}
	m.roles[panelID] = kept
	return nil
}

func (m *memSignupStore) RemoveRoleEverywhere(guildID, roleID string) ([]int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var touched []int64
	for id, p := range m.panels {
		if p.GuildID != guildID {
			continue
		}
		kept := m.roles[id][:0]
		for _, r := range m.roles[id] {
			if r.RoleID == roleID {
				touched = append(touched, id)
				continue
			}
			kept = append(kept, r)
		}
		m.roles[id] = kept
	}
	sort.Slice(touched, func(a, b int) bool { return touched[a] < touched[b] })
	return touched, nil
}
