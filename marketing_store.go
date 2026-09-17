package main

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

type MarketingStore interface {
	Lookup(email, publicURL string) (*MarketingProspect, error)
	InsertProspect(p *MarketingProspect) error
	UpdateProspect(p *MarketingProspect) error
	GetProspect(id string) (*MarketingProspect, error)
	ListProspects(filters prospectFilters) ([]MarketingProspect, error)
	InsertEvent(e *MarketingEvent) error
	ListEvents(prospectID, email string, limit int) ([]MarketingEvent, error)
	InsertOutreach(o *MarketingOutreach) error
	UpdateOutreachDiscordID(id, messageID string) error
	ListOutreach(filters outreachFilters) ([]MarketingOutreach, error)
	Stats() (*MarketingStats, error)
	Close() error
}

type MarketingStats struct {
	Prospects      int
	Outreach       int
	ByStatus       map[string]int
	ByNeed         map[string]int
	ByPersona      map[string]int
	ByCampaign     map[string]int
	EventsByType   map[string]int
	LastOutreachAt *time.Time
}

var marketingStore MarketingStore

type memMarketingStore struct {
	mu        sync.RWMutex
	prospects map[string]*MarketingProspect
	events    map[string]*MarketingEvent
	outreach  map[string]*MarketingOutreach
}

func newMemMarketingStore() *memMarketingStore {
	return &memMarketingStore{
		prospects: make(map[string]*MarketingProspect),
		events:    make(map[string]*MarketingEvent),
		outreach:  make(map[string]*MarketingOutreach),
	}
}

func (m *memMarketingStore) Close() error { return nil }

func (m *memMarketingStore) Lookup(email, publicURL string) (*MarketingProspect, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	email = normalizeEmail(email)
	publicURL = normalizeURL(publicURL)
	if email != "" {
		for _, p := range m.prospects {
			if normalizeEmail(p.Email) == email {
				return cloneProspect(p), nil
			}
		}
	}
	if publicURL != "" {
		for _, p := range m.prospects {
			if normalizeURL(p.PublicURL) == publicURL {
				return cloneProspect(p), nil
			}
		}
	}
	return nil, nil
}

func (m *memMarketingStore) identityTaken(email, publicURL, skipID string) bool {
	email = normalizeEmail(email)
	publicURL = normalizeURL(publicURL)
	if email != "" {
		for _, p := range m.prospects {
			if p.ID == skipID {
				continue
			}
			if normalizeEmail(p.Email) == email {
				return true
			}
		}
		return false
	}
	if publicURL != "" {
		for _, p := range m.prospects {
			if p.ID == skipID {
				continue
			}
			if normalizeEmail(p.Email) == "" && normalizeURL(p.PublicURL) == publicURL {
				return true
			}
		}
	}
	return false
}

func (m *memMarketingStore) InsertProspect(p *MarketingProspect) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.identityTaken(p.Email, p.PublicURL, "") {
		return errAlreadyExists
	}
	if p.ID == "" {
		p.ID = newMarketingID()
	}
	now := time.Now().UTC()
	if p.CreatedAt.IsZero() {
		p.CreatedAt = now
	}
	p.UpdatedAt = now
	if p.Tags == nil {
		p.Tags = []string{}
	}
	m.prospects[p.ID] = cloneProspect(p)
	return nil
}

func (m *memMarketingStore) UpdateProspect(p *MarketingProspect) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.prospects[p.ID]; !ok {
		return errNotFound
	}
	if m.identityTaken(p.Email, p.PublicURL, p.ID) {
		return errAlreadyExists
	}
	p.UpdatedAt = time.Now().UTC()
	m.prospects[p.ID] = cloneProspect(p)
	return nil
}

func (m *memMarketingStore) GetProspect(id string) (*MarketingProspect, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	p, ok := m.prospects[id]
	if !ok {
		return nil, errNotFound
	}
	return cloneProspect(p), nil
}

func (m *memMarketingStore) ListProspects(filters prospectFilters) ([]MarketingProspect, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	limit := clampLimit(filters.Limit, 50, 200)
	email := normalizeEmail(filters.Email)
	out := make([]MarketingProspect, 0)
	for _, p := range m.prospects {
		if filters.Status != "" && p.Status != filters.Status {
			continue
		}
		if filters.Persona != "" && p.Persona != filters.Persona {
			continue
		}
		if filters.Need != "" && p.Need != filters.Need {
			continue
		}
		if filters.Campaign != "" && p.Campaign != filters.Campaign {
			continue
		}
		if email != "" && normalizeEmail(p.Email) != email {
			continue
		}
		if filters.PublicURL != "" && normalizeURL(p.PublicURL) != normalizeURL(filters.PublicURL) {
			continue
		}
		out = append(out, *cloneProspect(p))
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *memMarketingStore) InsertEvent(e *MarketingEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.prospects[e.ProspectID]; !ok {
		return errNotFound
	}
	if e.ID == "" {
		e.ID = newMarketingID()
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now().UTC()
	}
	e.Metadata = normalizeMetadata(e.Metadata)
	cp := *e
	m.events[e.ID] = &cp
	return nil
}

func (m *memMarketingStore) ListEvents(prospectID, email string, limit int) ([]MarketingEvent, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	limit = clampLimit(limit, 50, 200)
	email = normalizeEmail(email)
	out := make([]MarketingEvent, 0)
	for _, e := range m.events {
		if prospectID != "" && e.ProspectID != prospectID {
			continue
		}
		if email != "" {
			p, ok := m.prospects[e.ProspectID]
			if !ok || normalizeEmail(p.Email) != email {
				continue
			}
		}
		out = append(out, *e)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].OccurredAt.After(out[j].OccurredAt)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *memMarketingStore) InsertOutreach(o *MarketingOutreach) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.prospects[o.ProspectID]; !ok {
		return errNotFound
	}
	if o.ID == "" {
		o.ID = newMarketingID()
	}
	if o.CreatedAt.IsZero() {
		o.CreatedAt = time.Now().UTC()
	}
	o.Metadata = normalizeMetadata(o.Metadata)
	cp := *o
	m.outreach[o.ID] = &cp
	return nil
}

func (m *memMarketingStore) UpdateOutreachDiscordID(id, messageID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	o, ok := m.outreach[id]
	if !ok {
		return errNotFound
	}
	o.DiscordMessageID = messageID
	return nil
}

func (m *memMarketingStore) ListOutreach(filters outreachFilters) ([]MarketingOutreach, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	limit := clampLimit(filters.Limit, 50, 200)
	email := normalizeEmail(filters.Email)
	out := make([]MarketingOutreach, 0)
	for _, o := range m.outreach {
		if filters.ProspectID != "" && o.ProspectID != filters.ProspectID {
			continue
		}
		if email != "" && normalizeEmail(o.Email) != email {
			continue
		}
		if filters.Campaign != "" && o.Campaign != filters.Campaign {
			continue
		}
		out = append(out, *o)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].OccurredAt.After(out[j].OccurredAt)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *memMarketingStore) Stats() (*MarketingStats, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	st := newMarketingStats()
	st.Prospects = len(m.prospects)
	st.Outreach = len(m.outreach)
	for _, p := range m.prospects {
		bumpCount(st.ByStatus, p.Status)
		bumpCount(st.ByNeed, p.Need)
		bumpCount(st.ByPersona, p.Persona)
		bumpCount(st.ByCampaign, p.Campaign)
	}
	for _, e := range m.events {
		bumpCount(st.EventsByType, e.Type)
	}
	for _, o := range m.outreach {
		if st.LastOutreachAt == nil || o.OccurredAt.After(*st.LastOutreachAt) {
			t := o.OccurredAt
			st.LastOutreachAt = &t
		}
	}
	return st, nil
}

func lookupProspect(store MarketingStore, id, email, publicURL string) (*MarketingProspect, error) {
	id = strings.TrimSpace(id)
	if id != "" {
		return store.GetProspect(id)
	}
	return store.Lookup(email, publicURL)
}

func newMarketingStats() *MarketingStats {
	return &MarketingStats{
		ByStatus:     map[string]int{},
		ByNeed:       map[string]int{},
		ByPersona:    map[string]int{},
		ByCampaign:   map[string]int{},
		EventsByType: map[string]int{},
	}
}

func bumpCount(m map[string]int, key string) {
	key = strings.TrimSpace(key)
	if key == "" {
		key = "(none)"
	}
	m[key]++
}

func formatCountMap(counts map[string]int, maxLines int) string {
	if len(counts) == 0 {
		return "none"
	}
	type kv struct {
		k string
		n int
	}
	rows := make([]kv, 0, len(counts))
	for k, n := range counts {
		rows = append(rows, kv{k, n})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].n == rows[j].n {
			return rows[i].k < rows[j].k
		}
		return rows[i].n > rows[j].n
	})
	if maxLines <= 0 {
		maxLines = 8
	}
	var b strings.Builder
	for i, row := range rows {
		if i >= maxLines {
			fmt.Fprintf(&b, "… +%d more", len(rows)-maxLines)
			break
		}
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "%s: %d", row.k, row.n)
	}
	return b.String()
}
