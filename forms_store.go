package main

import (
	"sort"
	"sync"
	"time"
)

// Where forms, their questions and the applications members send are kept. Postgres in
// production (forms_db.go); the in-memory store below backs the tests.

const (
	entryPending  = "pending"
	entryAccepted = "accepted"
	entryDeclined = "declined"
)

type appForm struct {
	ID              int64
	GuildID         string
	Name            string
	Intro           string
	ReviewChannelID string // where new applications go for staff
	AcceptRoleID    string // given when an application is accepted ("" for none)
	ReviewerRoleID  string // may accept and decline, besides Manage Server ("" for none)
	Open            bool
	ChatCategoryID  string // "" when the form opens no chats
	DMAccept        bool
	DMDecline       bool
	Emoji           string // on the form's button in a panel of forms
	Header          string // text at the top of the first pop-up
	Welcome         string // staff's own message in each applicant's chat
	CreatedBy       string
	CreatedAt       time.Time
}

type appEntry struct {
	ID              int64
	FormID          int64
	GuildID         string
	UserID          string
	Answers         string // JSON list of formAnswer
	Status          string
	SubmittedAt     time.Time
	ReviewChannelID string
	ReviewMessageID string
	DecidedBy       string
	Note            string
	ChatChannelID   string
}

type appVote struct {
	UserID string
	Vote   string // yes, no or unsure
}

type formsStore interface {
	CreateForm(f *appForm) (int64, error)
	// GetForm returns nil, nil when the form doesn't exist in that server.
	GetForm(guildID string, id int64) (*appForm, error)
	ListForms(guildID string) ([]appForm, error)
	// UpdateForm saves every field but the ID, server and creator.
	UpdateForm(f *appForm) error
	// DeleteForm removes a form with its questions, applications, votes and chat roles.
	DeleteForm(id int64) error

	Questions(formID int64) ([]formQuestion, error)
	AddQuestion(formID int64, q formQuestion) error
	RemoveQuestion(formID, questionID int64) error
	ReorderQuestions(formID int64, ids []int64) error

	ChatRoles(formID int64) ([]string, error)
	AddChatRole(formID int64, roleID string) error
	RemoveChatRole(formID int64, roleID string) error

	// Submit saves a new application. errAlreadyExists when the member has one waiting on this form.
	Submit(e *appEntry) (int64, error)
	GetEntry(guildID string, id int64) (*appEntry, error)
	// ListEntries: formID 0 is every form; status "" is every status.
	ListEntries(guildID string, formID int64, status string) ([]appEntry, error)
	LatestEntry(formID int64, userID string) (*appEntry, error)
	SetReviewMessage(entryID int64, channelID, messageID string) error
	SetChat(entryID int64, channelID string) error
	// EarlierChats are chat channels from the member's applications to this form, newest first.
	EarlierChats(formID int64, userID string) ([]string, error)
	// Decide accepts or declines a waiting application; false if it was already decided.
	Decide(entryID int64, status, by, note string) (bool, error)

	Votes(entryID int64) ([]appVote, error)
	// SetVote records, changes or (vote "") takes back a staff vote.
	SetVote(entryID int64, userID, vote string) error
}

// appForms is nil when the database is unavailable; /form then says so.
var appForms formsStore

type memFormsStore struct {
	mu        sync.Mutex
	next      int64
	forms     map[int64]*appForm
	questions map[int64][]formQuestion
	chatRoles map[int64][]string
	entries   map[int64]*appEntry
	votes     map[int64][]appVote
}

func newMemFormsStore() *memFormsStore {
	return &memFormsStore{
		forms: map[int64]*appForm{}, questions: map[int64][]formQuestion{}, chatRoles: map[int64][]string{},
		entries: map[int64]*appEntry{}, votes: map[int64][]appVote{},
	}
}

func (m *memFormsStore) id() int64 { m.next++; return m.next }

func (m *memFormsStore) CreateForm(f *appForm) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *f
	cp.ID = m.id()
	cp.CreatedAt = time.Now().UTC()
	m.forms[cp.ID] = &cp
	return cp.ID, nil
}

func (m *memFormsStore) GetForm(guildID string, id int64) (*appForm, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f, ok := m.forms[id]
	if !ok || f.GuildID != guildID {
		return nil, nil
	}
	cp := *f
	return &cp, nil
}

func (m *memFormsStore) ListForms(guildID string) ([]appForm, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []appForm{}
	for _, f := range m.forms {
		if f.GuildID == guildID {
			out = append(out, *f)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].ID < out[b].ID })
	return out, nil
}

func (m *memFormsStore) UpdateForm(f *appForm) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.forms[f.ID]
	if !ok {
		return errNotFound
	}
	cp := *f
	cp.GuildID, cp.CreatedBy, cp.CreatedAt = cur.GuildID, cur.CreatedBy, cur.CreatedAt
	m.forms[f.ID] = &cp
	return nil
}

func (m *memFormsStore) DeleteForm(id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for eid, e := range m.entries {
		if e.FormID == id {
			delete(m.votes, eid)
			delete(m.entries, eid)
		}
	}
	delete(m.forms, id)
	delete(m.questions, id)
	delete(m.chatRoles, id)
	return nil
}

func (m *memFormsStore) Questions(formID int64) ([]formQuestion, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]formQuestion(nil), m.questions[formID]...), nil
}

func (m *memFormsStore) AddQuestion(formID int64, q formQuestion) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	q.ID = m.id()
	m.questions[formID] = append(m.questions[formID], q)
	return nil
}

func (m *memFormsStore) RemoveQuestion(formID, questionID int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	kept := []formQuestion{}
	for _, q := range m.questions[formID] {
		if q.ID != questionID {
			kept = append(kept, q)
		}
	}
	m.questions[formID] = kept
	return nil
}

func (m *memFormsStore) ReorderQuestions(formID int64, ids []int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	byID := map[int64]formQuestion{}
	for _, q := range m.questions[formID] {
		byID[q.ID] = q
	}
	ordered := []formQuestion{}
	for _, id := range ids {
		if q, ok := byID[id]; ok {
			ordered = append(ordered, q)
			delete(byID, id)
		}
	}
	for _, q := range m.questions[formID] { // any not named keep their place at the end
		if _, left := byID[q.ID]; left {
			ordered = append(ordered, q)
		}
	}
	m.questions[formID] = ordered
	return nil
}

func (m *memFormsStore) ChatRoles(formID int64) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.chatRoles[formID]...), nil
}

func (m *memFormsStore) AddChatRole(formID int64, roleID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.chatRoles[formID] {
		if r == roleID {
			return nil
		}
	}
	m.chatRoles[formID] = append(m.chatRoles[formID], roleID)
	return nil
}

func (m *memFormsStore) RemoveChatRole(formID int64, roleID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	kept := []string{}
	for _, r := range m.chatRoles[formID] {
		if r != roleID {
			kept = append(kept, r)
		}
	}
	m.chatRoles[formID] = kept
	return nil
}

func (m *memFormsStore) Submit(e *appEntry) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, other := range m.entries {
		if other.FormID == e.FormID && other.UserID == e.UserID && other.Status == entryPending {
			return 0, errAlreadyExists
		}
	}
	cp := *e
	cp.ID = m.id()
	cp.Status = entryPending
	cp.SubmittedAt = time.Now().UTC()
	m.entries[cp.ID] = &cp
	return cp.ID, nil
}

func (m *memFormsStore) GetEntry(guildID string, id int64) (*appEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[id]
	if !ok || e.GuildID != guildID {
		return nil, nil
	}
	cp := *e
	return &cp, nil
}

func (m *memFormsStore) ListEntries(guildID string, formID int64, status string) ([]appEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []appEntry{}
	for _, e := range m.entries {
		if e.GuildID == guildID && (formID == 0 || e.FormID == formID) && (status == "" || e.Status == status) {
			out = append(out, *e)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].ID < out[b].ID })
	return out, nil
}

func (m *memFormsStore) LatestEntry(formID int64, userID string) (*appEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var latest *appEntry
	for _, e := range m.entries {
		if e.FormID == formID && e.UserID == userID && (latest == nil || e.ID > latest.ID) {
			latest = e
		}
	}
	if latest == nil {
		return nil, nil
	}
	cp := *latest
	return &cp, nil
}

func (m *memFormsStore) SetReviewMessage(entryID int64, channelID, messageID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.entries[entryID]; ok {
		e.ReviewChannelID, e.ReviewMessageID = channelID, messageID
	}
	return nil
}

func (m *memFormsStore) SetChat(entryID int64, channelID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.entries[entryID]; ok {
		e.ChatChannelID = channelID
	}
	return nil
}

func (m *memFormsStore) EarlierChats(formID int64, userID string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var found []*appEntry
	for _, e := range m.entries {
		if e.FormID == formID && e.UserID == userID && e.ChatChannelID != "" {
			found = append(found, e)
		}
	}
	sort.Slice(found, func(a, b int) bool { return found[a].ID > found[b].ID })
	out := make([]string, len(found))
	for n, e := range found {
		out[n] = e.ChatChannelID
	}
	return out, nil
}

func (m *memFormsStore) Decide(entryID int64, status, by, note string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[entryID]
	if !ok || e.Status != entryPending {
		return false, nil
	}
	e.Status, e.DecidedBy, e.Note = status, by, note
	return true, nil
}

func (m *memFormsStore) Votes(entryID int64) ([]appVote, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]appVote(nil), m.votes[entryID]...), nil
}

func (m *memFormsStore) SetVote(entryID int64, userID, vote string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	kept := []appVote{}
	for _, v := range m.votes[entryID] {
		if v.UserID != userID {
			kept = append(kept, v)
		}
	}
	if vote != "" {
		kept = append(kept, appVote{UserID: userID, Vote: vote})
	}
	m.votes[entryID] = kept
	return nil
}
