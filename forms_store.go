package main

import (
	"sort"
	"sync"
	"time"
)

// Where forms and their submissions are kept. Postgres in production (forms_db.go); the
// in-memory store below backs the tests.

const (
	subReceived = "received" // a form without review: nothing to decide
	subPending  = "pending"  // waiting for a reviewer
	subAccepted = "accepted"
	subDeclined = "declined"
)

// appForm is a form members fill in (game jam entries, sign-ups, feedback, applications…).
// Submissions are posted in its channel. With Review on, staff accept or decline them.
type appForm struct {
	ID             int64
	GuildID        string
	Name           string
	Intro          string // words above its button
	ChannelID      string // where submissions are posted
	ButtonLabel    string // "" for the default "📝 Fill in"
	Emoji          string // on its button in a panel
	Open           bool
	Header         string // text at the top of the first pop-up
	MaxPerMember   int    // submissions one member may have that aren't declined; 0 for no limit
	DMReceipt      bool   // DM the member a copy of what they sent
	Review         bool   // Accept, Decline and votes on each submission
	AcceptRoleID   string // given when a submission is accepted ("" for none)
	ReviewerRoleID string // may accept and decline, besides Manage Server ("" for none)
	DMAccept       bool
	DMDecline      bool
	CreatedBy      string
	CreatedAt      time.Time
}

type submission struct {
	ID          int64
	FormID      int64
	GuildID     string
	UserID      string
	Answers     string // JSON list of formAnswer
	Status      string
	SubmittedAt time.Time
	ChannelID   string // where it was posted
	MessageID   string
	DecidedBy   string
	Note        string
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
	// DeleteForm removes a form with its questions, submissions and votes.
	DeleteForm(id int64) error

	Questions(formID int64) ([]formQuestion, error)
	AddQuestion(formID int64, q formQuestion) error
	RemoveQuestion(formID, questionID int64) error
	ReorderQuestions(formID int64, ids []int64) error

	// Submit saves a submission with the status given (received or pending).
	Submit(sub *submission) (int64, error)
	GetSubmission(guildID string, id int64) (*submission, error)
	// ListSubmissions: formID 0 is every form; status "" is every status. Newest first.
	ListSubmissions(guildID string, formID int64, status string) ([]submission, error)
	// SubmissionsOf is one member's submissions to a form, newest first.
	SubmissionsOf(formID int64, userID string) ([]submission, error)
	SetSubmissionMessage(id int64, channelID, messageID string) error
	// Decide accepts or declines a pending submission; false if it was already decided.
	Decide(id int64, status, by, note string) (bool, error)

	Votes(subID int64) ([]appVote, error)
	// SetVote records, changes or (vote "") takes back a staff vote.
	SetVote(subID int64, userID, vote string) error
}

// appForms is nil when the database is unavailable; /form then says so.
var appForms formsStore

type memFormsStore struct {
	mu        sync.Mutex
	next      int64
	forms     map[int64]*appForm
	questions questionLists
	subs      map[int64]*submission
	votes     map[int64][]appVote
}

func newMemFormsStore() *memFormsStore {
	return &memFormsStore{forms: map[int64]*appForm{}, questions: questionLists{}, subs: map[int64]*submission{}, votes: map[int64][]appVote{}}
}

func (m *memFormsStore) id() int64 { m.next++; return m.next }

func (m *memFormsStore) CreateForm(f *appForm) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *f
	cp.ID, cp.CreatedAt = m.id(), time.Now().UTC()
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
	for sid, sub := range m.subs {
		if sub.FormID == id {
			delete(m.votes, sid)
			delete(m.subs, sid)
		}
	}
	delete(m.forms, id)
	m.questions.drop(id)
	return nil
}

func (m *memFormsStore) Questions(formID int64) ([]formQuestion, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.questions.get(formID), nil
}

func (m *memFormsStore) AddQuestion(formID int64, q formQuestion) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	q.ID = m.id()
	m.questions.add(formID, q)
	return nil
}

func (m *memFormsStore) RemoveQuestion(formID, questionID int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.questions.remove(formID, questionID)
	return nil
}

func (m *memFormsStore) ReorderQuestions(formID int64, ids []int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.questions.reorder(formID, ids)
	return nil
}

func (m *memFormsStore) Submit(sub *submission) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *sub
	cp.ID, cp.SubmittedAt = m.id(), time.Now().UTC()
	if cp.Status == "" {
		cp.Status = subReceived
	}
	m.subs[cp.ID] = &cp
	return cp.ID, nil
}

func (m *memFormsStore) GetSubmission(guildID string, id int64) (*submission, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sub, ok := m.subs[id]
	if !ok || sub.GuildID != guildID {
		return nil, nil
	}
	cp := *sub
	return &cp, nil
}

func (m *memFormsStore) filter(keep func(*submission) bool) []submission {
	out := []submission{}
	for _, sub := range m.subs {
		if keep(sub) {
			out = append(out, *sub)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].ID > out[b].ID })
	return out
}

func (m *memFormsStore) ListSubmissions(guildID string, formID int64, status string) ([]submission, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.filter(func(sub *submission) bool {
		return sub.GuildID == guildID && (formID == 0 || sub.FormID == formID) && (status == "" || sub.Status == status)
	}), nil
}

func (m *memFormsStore) SubmissionsOf(formID int64, userID string) ([]submission, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.filter(func(sub *submission) bool { return sub.FormID == formID && sub.UserID == userID }), nil
}

func (m *memFormsStore) SetSubmissionMessage(id int64, channelID, messageID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if sub, ok := m.subs[id]; ok {
		sub.ChannelID, sub.MessageID = channelID, messageID
	}
	return nil
}

func (m *memFormsStore) Decide(id int64, status, by, note string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sub, ok := m.subs[id]
	if !ok || sub.Status != subPending {
		return false, nil
	}
	sub.Status, sub.DecidedBy, sub.Note = status, by, note
	return true, nil
}

func (m *memFormsStore) Votes(subID int64) ([]appVote, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]appVote(nil), m.votes[subID]...), nil
}

func (m *memFormsStore) SetVote(subID int64, userID, vote string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	kept := []appVote{}
	for _, v := range m.votes[subID] {
		if v.UserID != userID {
			kept = append(kept, v)
		}
	}
	if vote != "" {
		kept = append(kept, appVote{UserID: userID, Vote: vote})
	}
	m.votes[subID] = kept
	return nil
}
