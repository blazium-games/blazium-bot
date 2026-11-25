package main

import (
	"sort"
	"sync"
	"time"
)

// Where ticket types, their intake questions and the tickets themselves are kept.
// Postgres in production (tickets_db.go); the in-memory store below backs the tests.

const (
	ticketOpen    = "open"
	ticketClosed  = "closed"
	ticketDeleted = "deleted" // its channel was deleted; the record and transcript stay
)

type ticketType struct {
	ID           int64
	GuildID      string
	Name         string
	Intro        string // words above the Open button
	CategoryID   string // where its ticket channels are made
	LogChannelID string // staff-only channel for opened/claimed/closed notes and transcripts
	Open         bool   // members can open new tickets
	Emoji        string // on the type's button
	Header       string // text at the top of the first intake pop-up
	Welcome      string // staff's own message posted in each new ticket
	MaxOpen      int    // open tickets one member may have of this type at once
	DMTranscript bool   // send the member the transcript when their ticket closes
	PingStaff    bool   // ping the staff roles when a ticket opens
	CreatedBy    string
	CreatedAt    time.Time
}

type ticket struct {
	ID          int64
	GuildID     string
	Number      int64 // counts up per server: ticket-0042
	TypeID      int64
	UserID      string // who opened it
	ChannelID   string
	Status      string
	Answers     string // JSON list of formAnswer (the intake questions)
	OpenedAt    time.Time
	ClaimedBy   string
	ClosedBy    string
	CloseReason string
	ClosedAt    *time.Time
}

type ticketsStore interface {
	CreateType(t *ticketType) (int64, error)
	// GetType returns nil, nil when the type doesn't exist in that server.
	GetType(guildID string, id int64) (*ticketType, error)
	ListTypes(guildID string) ([]ticketType, error)
	// UpdateType saves every field but the ID, server and creator.
	UpdateType(t *ticketType) error
	// DeleteType removes a type with its questions, staff roles and ticket records.
	DeleteType(id int64) error

	Questions(typeID int64) ([]formQuestion, error)
	AddQuestion(typeID int64, q formQuestion) error
	RemoveQuestion(typeID, questionID int64) error
	ReorderQuestions(typeID int64, ids []int64) error

	StaffRoles(typeID int64) ([]string, error)
	AddStaffRole(typeID int64, roleID string) error
	RemoveStaffRole(typeID int64, roleID string) error

	// CreateTicket saves a new open ticket and gives it the server's next number.
	CreateTicket(t *ticket) (id, number int64, err error)
	GetTicket(guildID string, id int64) (*ticket, error)
	// TicketByChannel is the ticket whose channel this is, or nil.
	TicketByChannel(channelID string) (*ticket, error)
	// ListTickets: typeID 0 is every type; status "" is every status. Newest first.
	ListTickets(guildID string, typeID int64, status string) ([]ticket, error)
	OpenTicketsOf(typeID int64, userID string) ([]ticket, error)
	SetTicketChannel(id int64, channelID string) error
	SetClaim(id int64, userID string) error
	// Close closes an open ticket; false if it wasn't open.
	Close(id int64, by, reason string) (bool, error)
	// Reopen opens a closed ticket again; false if it wasn't closed.
	Reopen(id int64) (bool, error)
	MarkDeleted(id int64) error
	// DeleteTicket removes a ticket record (used when its channel couldn't be made).
	DeleteTicket(id int64) error
}

// tickets is nil when the database is unavailable; /ticket then says so.
var tickets ticketsStore

type memTicketsStore struct {
	mu        sync.Mutex
	next      int64
	types     map[int64]*ticketType
	questions questionLists
	staff     map[int64][]string
	tickets   map[int64]*ticket
}

func newMemTicketsStore() *memTicketsStore {
	return &memTicketsStore{
		types: map[int64]*ticketType{}, questions: questionLists{},
		staff: map[int64][]string{}, tickets: map[int64]*ticket{},
	}
}

func (m *memTicketsStore) id() int64 { m.next++; return m.next }

func (m *memTicketsStore) CreateType(t *ticketType) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *t
	cp.ID = m.id()
	cp.CreatedAt = time.Now().UTC()
	m.types[cp.ID] = &cp
	return cp.ID, nil
}

func (m *memTicketsStore) GetType(guildID string, id int64) (*ticketType, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.types[id]
	if !ok || t.GuildID != guildID {
		return nil, nil
	}
	cp := *t
	return &cp, nil
}

func (m *memTicketsStore) ListTypes(guildID string) ([]ticketType, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []ticketType{}
	for _, t := range m.types {
		if t.GuildID == guildID {
			out = append(out, *t)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].ID < out[b].ID })
	return out, nil
}

func (m *memTicketsStore) UpdateType(t *ticketType) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.types[t.ID]
	if !ok {
		return errNotFound
	}
	cp := *t
	cp.GuildID, cp.CreatedBy, cp.CreatedAt = cur.GuildID, cur.CreatedBy, cur.CreatedAt
	m.types[t.ID] = &cp
	return nil
}

func (m *memTicketsStore) DeleteType(id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for tid, t := range m.tickets {
		if t.TypeID == id {
			delete(m.tickets, tid)
		}
	}
	delete(m.types, id)
	delete(m.questions, id)
	delete(m.staff, id)
	return nil
}

func (m *memTicketsStore) Questions(typeID int64) ([]formQuestion, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.questions.get(typeID), nil
}

func (m *memTicketsStore) AddQuestion(typeID int64, q formQuestion) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	q.ID = m.id()
	m.questions.add(typeID, q)
	return nil
}

func (m *memTicketsStore) RemoveQuestion(typeID, questionID int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.questions.remove(typeID, questionID)
	return nil
}

func (m *memTicketsStore) ReorderQuestions(typeID int64, ids []int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.questions.reorder(typeID, ids)
	return nil
}

func (m *memTicketsStore) StaffRoles(typeID int64) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.staff[typeID]...), nil
}

func (m *memTicketsStore) AddStaffRole(typeID int64, roleID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.staff[typeID] {
		if r == roleID {
			return nil
		}
	}
	m.staff[typeID] = append(m.staff[typeID], roleID)
	return nil
}

func (m *memTicketsStore) RemoveStaffRole(typeID int64, roleID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	kept := []string{}
	for _, r := range m.staff[typeID] {
		if r != roleID {
			kept = append(kept, r)
		}
	}
	m.staff[typeID] = kept
	return nil
}

func (m *memTicketsStore) CreateTicket(t *ticket) (int64, int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var number int64
	for _, other := range m.tickets {
		if other.GuildID == t.GuildID && other.Number > number {
			number = other.Number
		}
	}
	cp := *t
	cp.ID, cp.Number, cp.Status, cp.OpenedAt = m.id(), number+1, ticketOpen, time.Now().UTC()
	m.tickets[cp.ID] = &cp
	return cp.ID, cp.Number, nil
}

func (m *memTicketsStore) GetTicket(guildID string, id int64) (*ticket, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tickets[id]
	if !ok || t.GuildID != guildID {
		return nil, nil
	}
	cp := *t
	return &cp, nil
}

func (m *memTicketsStore) TicketByChannel(channelID string) (*ticket, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.tickets {
		if channelID != "" && t.ChannelID == channelID {
			cp := *t
			return &cp, nil
		}
	}
	return nil, nil
}

func (m *memTicketsStore) ListTickets(guildID string, typeID int64, status string) ([]ticket, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []ticket{}
	for _, t := range m.tickets {
		if t.GuildID == guildID && (typeID == 0 || t.TypeID == typeID) && (status == "" || t.Status == status) {
			out = append(out, *t)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].ID > out[b].ID })
	return out, nil
}

func (m *memTicketsStore) OpenTicketsOf(typeID int64, userID string) ([]ticket, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []ticket{}
	for _, t := range m.tickets {
		if t.TypeID == typeID && t.UserID == userID && t.Status == ticketOpen {
			out = append(out, *t)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].ID < out[b].ID })
	return out, nil
}

func (m *memTicketsStore) with(id int64, change func(t *ticket) bool) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tickets[id]
	if !ok {
		return false, nil
	}
	return change(t), nil
}

func (m *memTicketsStore) SetTicketChannel(id int64, channelID string) error {
	_, err := m.with(id, func(t *ticket) bool { t.ChannelID = channelID; return true })
	return err
}

func (m *memTicketsStore) SetClaim(id int64, userID string) error {
	_, err := m.with(id, func(t *ticket) bool { t.ClaimedBy = userID; return true })
	return err
}

func (m *memTicketsStore) Close(id int64, by, reason string) (bool, error) {
	return m.with(id, func(t *ticket) bool {
		if t.Status != ticketOpen {
			return false
		}
		now := time.Now().UTC()
		t.Status, t.ClosedBy, t.CloseReason, t.ClosedAt = ticketClosed, by, reason, &now
		return true
	})
}

func (m *memTicketsStore) Reopen(id int64) (bool, error) {
	return m.with(id, func(t *ticket) bool {
		if t.Status != ticketClosed {
			return false
		}
		t.Status = ticketOpen
		return true
	})
}

func (m *memTicketsStore) MarkDeleted(id int64) error {
	_, err := m.with(id, func(t *ticket) bool { t.Status = ticketDeleted; return true })
	return err
}

func (m *memTicketsStore) DeleteTicket(id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.tickets, id)
	return nil
}
