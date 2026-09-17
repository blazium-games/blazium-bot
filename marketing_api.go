package main

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"
)

func initMarketing() {
	loadMarketingConfig()
	if len(marketingCfg.token) != 128 {
		appLogger.Warn("GROKBOT_ACCESS_TOKEN is missing or not 128 characters; marketing API disabled")
		return
	}
	if marketingCfg.connString == "" {
		appLogger.Warn("POSTGRES_CON_STRING is not set; marketing API disabled")
		return
	}
	store, err := newPostgresMarketingStore(marketingCfg.connString)
	if err != nil {
		appLogger.Errorf("marketing postgres unavailable: %v", err)
		return
	}
	marketingStore = store
	appLogger.Info("Marketing API ready")
}

func registerMarketingRoutes(r *mux.Router) {
	s := r.PathPrefix("/v1/marketing").Subrouter()
	s.Use(marketingAuthMiddleware)
	s.HandleFunc("/example", handleMarketingExample).Methods(http.MethodGet)
	s.HandleFunc("/exists", handleMarketingExists).Methods(http.MethodGet)
	s.HandleFunc("/lookup", handleMarketingLookup).Methods(http.MethodPost)
	s.HandleFunc("/prospects/ensure", handleMarketingEnsure).Methods(http.MethodPost)
	s.HandleFunc("/prospects/{id}/outreach", handleMarketingProspectOutreach).Methods(http.MethodGet)
	s.HandleFunc("/prospects/{id}", handleMarketingProspectByID).Methods(http.MethodGet, http.MethodPatch)
	s.HandleFunc("/prospects", handleMarketingProspects).Methods(http.MethodGet, http.MethodPost)
	s.HandleFunc("/events", handleMarketingEvents).Methods(http.MethodGet, http.MethodPost)
	s.HandleFunc("/outreach", handleMarketingOutreachList).Methods(http.MethodGet)
}

func marketingAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !marketingAPIReady() {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "marketing api unavailable"})
			return
		}
		if !marketingTokenOK(extractMarketingToken(r)) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func extractMarketingToken(r *http.Request) string {
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(auth) > 7 && strings.EqualFold(auth[:7], "Bearer ") {
		return strings.TrimSpace(auth[7:])
	}
	return strings.TrimSpace(r.Header.Get("X-Grokbot-Token"))
}

func marketingTokenOK(got string) bool {
	want := marketingCfg.token
	if len(got) != len(want) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if body == nil {
		return
	}
	_ = json.NewEncoder(w).Encode(body)
}

func decodeJSON(r *http.Request, dest any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	if err := dec.Decode(dest); err != nil {
		return err
	}
	return nil
}

func handleMarketingExample(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(marketingExampleJSON)
}

func handleMarketingExists(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	writeExists(w, q.Get("email"), q.Get("public_url"))
}

func handleMarketingLookup(w http.ResponseWriter, r *http.Request) {
	var body marketingLookup
	if err := decodeJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	writeExists(w, body.Email, body.PublicURL)
}

func writeExists(w http.ResponseWriter, email, publicURL string) {
	if strings.TrimSpace(email) == "" && strings.TrimSpace(publicURL) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "email or public_url is required"})
		return
	}
	p, err := marketingStore.Lookup(email, publicURL)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"exists":   p != nil,
		"prospect": p,
	})
}

func handleMarketingProspects(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		q := r.URL.Query()
		limit, _ := strconv.Atoi(q.Get("limit"))
		list, err := marketingStore.ListProspects(prospectFilters{
			Status:    strings.TrimSpace(q.Get("status")),
			Persona:   strings.TrimSpace(q.Get("persona")),
			Need:      strings.TrimSpace(q.Get("need")),
			Campaign:  strings.TrimSpace(q.Get("campaign")),
			Email:     strings.TrimSpace(q.Get("email")),
			PublicURL: strings.TrimSpace(q.Get("public_url")),
			Limit:     limit,
		})
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if list == nil {
			list = []MarketingProspect{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"prospects": list})
	case http.MethodPost:
		payload, err := decodePayload(r)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		created, event, outreach, err := createProspect(payload, false)
		if errors.Is(err, errAlreadyExists) {
			existing, _ := marketingStore.Lookup(payload.Prospect.Email, payload.Prospect.PublicURL)
			writeJSON(w, http.StatusConflict, map[string]any{"error": "already exists", "prospect": existing})
			return
		}
		if err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, errNotFound) {
				status = http.StatusNotFound
			}
			writeJSON(w, status, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{
			"created":  true,
			"prospect": created,
			"event":    event,
			"outreach": outreach,
		})
	}
}

func handleMarketingEnsure(w http.ResponseWriter, r *http.Request) {
	payload, err := decodePayload(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	existing, err := marketingStore.Lookup(payload.Prospect.Email, payload.Prospect.PublicURL)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	created := false
	var prospect *MarketingProspect
	if existing == nil {
		prospect, _, _, err = createProspect(payload, true)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		created = true
	} else {
		prospect = existing
		if payload.Overwrite {
			copyProspectFields(prospect, &payload.Prospect)
			if err := validateProspectInput(prospect); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			if err := marketingStore.UpdateProspect(prospect); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
		}
		if payload.Event != nil {
			_, _, err = recordEvent(prospect, payload.Event)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			prospect, _ = marketingStore.GetProspect(prospect.ID)
		}
	}
	var event *MarketingEvent
	var outreach *MarketingOutreach
	if payload.Event != nil && prospect != nil {
		events, _ := marketingStore.ListEvents(prospect.ID, "", 1)
		if len(events) > 0 {
			event = &events[0]
		}
		rows, _ := marketingStore.ListOutreach(outreachFilters{ProspectID: prospect.ID, Limit: 1})
		if len(rows) > 0 {
			outreach = &rows[0]
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"created":  created,
		"prospect": prospect,
		"event":    event,
		"outreach": outreach,
	})
}

func handleMarketingProspectByID(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	p, err := marketingStore.GetProspect(id)
	if errors.Is(err, errNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, p)
		return
	}
	var patch marketingProspectPatch
	if err := decodeJSON(r, &patch); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if err := applyProspectPatch(p, patch); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := marketingStore.UpdateProspect(p); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, errAlreadyExists) {
			status = http.StatusConflict
		}
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func handleMarketingEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		q := r.URL.Query()
		limit, _ := strconv.Atoi(q.Get("limit"))
		list, err := marketingStore.ListEvents(strings.TrimSpace(q.Get("prospect_id")), strings.TrimSpace(q.Get("email")), limit)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if list == nil {
			list = []MarketingEvent{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"events": list})
		return
	}
	var req marketingEventRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	p, err := lookupProspect(marketingStore, req.ProspectID, req.Email, req.PublicURL)
	if errors.Is(err, errNotFound) || p == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "prospect not found"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	ev, err := eventFromRequest(req)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	event, outreach, err := recordEvent(p, ev)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	p, _ = marketingStore.GetProspect(p.ID)
	writeJSON(w, http.StatusCreated, map[string]any{
		"prospect": p,
		"event":    event,
		"outreach": outreach,
	})
}

func handleMarketingOutreachList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	list, err := marketingStore.ListOutreach(outreachFilters{
		Email:      strings.TrimSpace(q.Get("email")),
		ProspectID: strings.TrimSpace(q.Get("prospect_id")),
		Campaign:   strings.TrimSpace(q.Get("campaign")),
		Limit:      limit,
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if list == nil {
		list = []MarketingOutreach{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"outreach": list})
}

func handleMarketingProspectOutreach(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	if _, err := marketingStore.GetProspect(id); errors.Is(err, errNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	} else if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	list, err := marketingStore.ListOutreach(outreachFilters{ProspectID: id, Limit: limit})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if list == nil {
		list = []MarketingOutreach{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"outreach": list})
}

func decodePayload(r *http.Request) (*MarketingPayload, error) {
	var payload MarketingPayload
	if err := decodeJSON(r, &payload); err != nil {
		return nil, errors.New("invalid json")
	}
	if err := validateProspectInput(&payload.Prospect); err != nil {
		return nil, err
	}
	if payload.Event != nil {
		if err := validateEventInput(payload.Event); err != nil {
			return nil, err
		}
	}
	return &payload, nil
}

func createProspect(payload *MarketingPayload, recordOptionalEvent bool) (*MarketingProspect, *MarketingEvent, *MarketingOutreach, error) {
	p := cloneProspect(&payload.Prospect)
	p.ID = newMarketingID()
	p.TouchCount = 0
	p.CreatedAt = time.Now().UTC()
	p.UpdatedAt = p.CreatedAt
	if err := marketingStore.InsertProspect(p); err != nil {
		return nil, nil, nil, err
	}
	onProspectCreated(p)
	if !recordOptionalEvent || payload.Event == nil {
		return p, nil, nil, nil
	}
	event, outreach, err := recordEvent(p, payload.Event)
	if err != nil {
		return p, nil, nil, err
	}
	if updated, err := marketingStore.GetProspect(p.ID); err == nil {
		p = updated
	}
	return p, event, outreach, nil
}

func recordEvent(prospect *MarketingProspect, input *MarketingEvent) (*MarketingEvent, *MarketingOutreach, error) {
	if err := validateEventInput(input); err != nil {
		return nil, nil, err
	}
	event := *input
	event.ID = newMarketingID()
	event.ProspectID = prospect.ID
	event.CreatedAt = time.Now().UTC()
	if event.Type == "send" && event.TouchNumber == 0 {
		event.TouchNumber = prospect.TouchCount + 1
	}
	if event.Campaign == "" {
		event.Campaign = prospect.Campaign
	}
	if err := marketingStore.InsertEvent(&event); err != nil {
		return nil, nil, err
	}

	now := event.OccurredAt
	prospect.LastEventAt = &now
	prospect.Status = statusAfterEvent(prospect.Status, event.Type)
	if event.Type == "send" {
		prospect.TouchCount++
		prospect.LastOutreachAt = &now
	}
	if err := marketingStore.UpdateProspect(prospect); err != nil {
		return nil, nil, err
	}

	var outreach *MarketingOutreach
	if event.Type == "send" {
		o := &MarketingOutreach{
			ID:          newMarketingID(),
			ProspectID:  prospect.ID,
			Email:       prospect.Email,
			DisplayName: prospect.DisplayName,
			Campaign:    event.Campaign,
			Channel:     event.Channel,
			Subject:     event.Subject,
			TouchNumber: event.TouchNumber,
			Status:      prospect.Status,
			OccurredAt:  event.OccurredAt,
			Metadata:    event.Metadata,
			CreatedAt:   time.Now().UTC(),
		}
		if err := marketingStore.InsertOutreach(o); err != nil {
			return &event, nil, err
		}
		outreach = o
		onOutreachRecorded(prospect, o)
	}
	return &event, outreach, nil
}
