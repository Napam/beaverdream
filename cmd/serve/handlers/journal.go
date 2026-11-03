package handlers

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/Napam/beaverdream/db/generated/sqlc"
	"github.com/Napam/beaverdream/lib/webmaster"
	"github.com/go-chi/chi/v5"
)

// VisitorCookieName is an opaque id the server draws once per browser. Display
// names are not unique, so this is what can hold a seat in a game.
const VisitorCookieName = "beaverdream_visitor"

const (
	visitorCookieAge = 365 * 24 * time.Hour
	maxJournalBody   = 4096
	// maxJournalRead matches the largest key the model can declare, so a
	// first load gets the whole feed instead of its oldest page.
	maxJournalRead    = 1000
	maxAlertsPerBurst = 5
	alertBurstWindow  = 10 * time.Minute
	alertRepeatWindow = 2 * time.Minute
	// A game click is a write, so a plain cooldown would stall play. A small
	// token bucket allows a burst, then settles at journalRate per second.
	journalBurst = 12
	journalRate  = 8
)

// journalBucket is one visitor's short-term write allowance for one key.
type journalBucket struct {
	tokens float64
	at     time.Time
}

// journalRow is one event as the page sees it. The payload stays raw JSON: the
// server never interprets it.
type journalRow struct {
	ID      int64           `json:"id"`
	Visitor string          `json:"visitor"`
	Payload json.RawMessage `json:"payload"`
	Match   int64           `json:"match"`
	Rules   int64           `json:"rules"`
	At      string          `json:"at"`
}

// JournalReadResponse is what a page gets when it asks for events. Rows carry
// raw payloads the server never interprets.
type JournalReadResponse struct {
	Key   string       `json:"key"`
	Match int64        `json:"match"`
	Rules int64        `json:"rules"`
	Rows  []journalRow `json:"rows"`
}

func journalRowFrom(row sqlc.Journal) journalRow {
	payload := json.RawMessage(row.Payload)
	if !json.Valid(payload) {
		payload = json.RawMessage("{}")
	}
	return journalRow{
		ID:      row.ID,
		Visitor: row.Visitor,
		Payload: payload,
		Match:   row.Match,
		Rules:   row.Rules,
		At:      row.CreatedAt,
	}
}

// visitorFromRequest returns the browser's opaque id, drawing one on the first
// visit. The value is not a credential: it only separates one browser from
// another.
func visitorFromRequest(w http.ResponseWriter, r *http.Request) string {
	if cookie, err := r.Cookie(VisitorCookieName); err == nil && validVisitorID(cookie.Value) {
		return cookie.Value
	}
	id := newVisitorID()
	if w != nil {
		http.SetCookie(w, &http.Cookie{
			Name:     VisitorCookieName,
			Value:    id,
			Path:     "/",
			MaxAge:   int(visitorCookieAge.Seconds()),
			Expires:  time.Now().Add(visitorCookieAge),
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
		})
	}
	return id
}

func newVisitorID() string {
	buf := make([]byte, 8)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

func validVisitorID(id string) bool {
	if len(id) != 16 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

// publicVisitor is what the world sees: rows and the page carry this, never
// the cookie itself. A visitor id in the log cannot be copied into another
// browser to take someone's seat, because the cookie it comes from is
// HttpOnly and never published.
func publicVisitor(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:8])
}

// JournalAppend stores one event. The page owns the meaning; the server owns
// order, identity, the match boundary and the rules stamp.
func (h *Handlers) JournalAppend(w http.ResponseWriter, r *http.Request) {
	author := identityFromRequest(r)
	if author == "" {
		writeStateError(w, http.StatusUnauthorized, "pick a name first")
		return
	}
	visitor := visitorFromRequest(w, r)
	key := chi.URLParam(r, "key")

	site, err := h.queries.CurrentSite(r.Context())
	if err != nil {
		writeStateError(w, http.StatusInternalServerError, "could not load the board")
		return
	}
	schema := webmaster.ParseJournalSchema(site.JournalSchema)
	def, ok := schema[key]
	if !ok {
		writeStateError(w, http.StatusNotFound, "unknown journal key")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxJournalBody)
	var in struct {
		Payload json.RawMessage `json:"payload"`
		Match   int64           `json:"match"`
		Rules   int64           `json:"rules"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeStateError(w, http.StatusBadRequest, "bad event")
		return
	}

	// The page says which rules its code was written for. A different number
	// is a tab still running a replaced reducer: its payload would land under
	// rules it does not follow, so it is refused and the page reloads.
	rules := int64(def.Rules)
	if rules <= 0 {
		rules = 1
	}
	if in.Rules != rules {
		writeStateError(w, http.StatusConflict, "the page is out of date")
		return
	}

	payload, err := webmaster.CleanJournalPayload(in.Payload)
	if err != nil {
		writeStateError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !h.allowJournalWrite(visitor, key) {
		writeStateError(w, http.StatusTooManyRequests, "slow down")
		return
	}

	// The mutex makes reading the newest match and appending the event one
	// step, so two visitors cannot open the same next match at once.
	h.stateMu.Lock()
	latest, err := h.queries.JournalLatest(r.Context(), key)
	if err != nil {
		h.stateMu.Unlock()
		writeStateError(w, http.StatusInternalServerError, "could not store that")
		return
	}
	current := latest.CurrentMatch
	if current <= 0 {
		current = 1
	}
	match := in.Match
	if match <= 0 {
		match = current
	}
	if match < current || match > current+1 {
		h.stateMu.Unlock()
		writeStateError(w, http.StatusBadRequest, webmaster.JournalMatchClosed)
		return
	}
	row, err := h.queries.AppendJournal(r.Context(), sqlc.AppendJournalParams{
		Key:     key,
		Visitor: publicVisitor(visitor),
		Payload: payload,
		Match:   match,
		Rules:   rules,
	})
	if err == nil {
		err = h.queries.TrimJournal(r.Context(), sqlc.TrimJournalParams{
			Key:    key,
			Offset: int64(def.Max),
		})
	}
	h.stateMu.Unlock()
	if err != nil {
		h.logger.Error("Failed to append to the journal", slog.Any("error", err))
		writeStateError(w, http.StatusInternalServerError, "could not store that")
		return
	}
	h.touchActivity()

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(journalRowFrom(row))
}

// JournalRead returns events after a cursor. The page filters by match and
// rules; the server reports the newest match so a page can notice a rematch.
func (h *Handlers) JournalRead(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")

	site, err := h.queries.CurrentSite(r.Context())
	if err != nil {
		writeStateError(w, http.StatusInternalServerError, "could not load the board")
		return
	}
	schema := webmaster.ParseJournalSchema(site.JournalSchema)
	def, ok := schema[key]
	if !ok {
		writeStateError(w, http.StatusNotFound, "unknown journal key")
		return
	}

	since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	if since < 0 {
		since = 0
	}
	rows, err := h.queries.ListJournalSince(r.Context(), sqlc.ListJournalSinceParams{
		Key:   key,
		ID:    since,
		Limit: maxJournalRead,
	})
	if err != nil {
		h.logger.Error("Failed to read the journal", slog.Any("error", err))
		writeStateError(w, http.StatusInternalServerError, "could not read that")
		return
	}
	latest, err := h.queries.JournalLatest(r.Context(), key)
	if err != nil {
		h.logger.Error("Failed to read journal state", slog.Any("error", err))
		writeStateError(w, http.StatusInternalServerError, "could not read that")
		return
	}
	match := latest.CurrentMatch
	if match <= 0 {
		match = 1
	}
	rules := int64(def.Rules)
	if rules <= 0 {
		rules = 1
	}

	out := make([]journalRow, len(rows))
	for i, row := range rows {
		out[i] = journalRowFrom(row)
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(JournalReadResponse{
		Key:   key,
		Match: match,
		Rules: rules,
		Rows:  out,
	})
}

// ClientError is the page reporting its own failure. The model cannot read a
// browser console, so a thrown script is written into the room as an event the
// webmaster sees on its next turn.
func (h *Handlers) ClientError(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 2048)
	var in struct {
		Message string `json:"message"`
		Version int64  `json:"version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	message := webmaster.CleanStateValue(in.Message)
	if message == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// An error from a page that has since been replaced is about code nobody
	// is served any more. Writing it into the room would send the webmaster
	// to fix a page that is already gone, and it would spend the alert cap
	// that real errors need.
	if in.Version > 0 {
		if raw, err := h.queries.CurrentVersionID(r.Context()); err == nil {
			if current, ok := raw.(int64); ok && current != in.Version {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
	}
	if !h.allowAlert(message) {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	if _, err := h.queries.CreateTurn(r.Context(), sqlc.CreateTurnParams{
		Kind:   webmaster.TurnAlert,
		Author: "browser",
		Body:   message,
	}); err != nil {
		h.logger.Error("Failed to record a browser error", slog.Any("error", err))
	}
	w.WriteHeader(http.StatusNoContent)
}

// allowJournalWrite keeps one visitor from flooding one feed without stalling
// a game: a burst is allowed, then it refills at journalRate per second.
func (h *Handlers) allowJournalWrite(visitor, key string) bool {
	now := time.Now()
	slot := visitor + "\x00" + key
	h.liveMu.Lock()
	defer h.liveMu.Unlock()
	if h.journalBucket == nil {
		h.journalBucket = make(map[string]*journalBucket)
	}
	if len(h.journalBucket) > 256 {
		for slot, old := range h.journalBucket {
			if now.Sub(old.at) > time.Minute {
				delete(h.journalBucket, slot)
			}
		}
	}
	bucket, ok := h.journalBucket[slot]
	if !ok {
		bucket = &journalBucket{tokens: journalBurst, at: now}
		h.journalBucket[slot] = bucket
	}
	bucket.tokens += now.Sub(bucket.at).Seconds() * journalRate
	bucket.at = now
	if bucket.tokens > journalBurst {
		bucket.tokens = journalBurst
	}
	if bucket.tokens < 1 {
		return false
	}
	bucket.tokens--
	return true
}

// allowAlert collapses a burst of identical script errors into one event and
// caps the total, so a render loop cannot fill the room with its own noise.
func (h *Handlers) allowAlert(message string) bool {
	now := time.Now()
	h.liveMu.Lock()
	defer h.liveMu.Unlock()
	if h.alertSeen == nil {
		h.alertSeen = make(map[string]time.Time)
	}
	if at, ok := h.alertSeen[message]; ok && now.Sub(at) < alertRepeatWindow {
		return false
	}
	recent := 0
	for text, at := range h.alertSeen {
		if now.Sub(at) > alertBurstWindow {
			delete(h.alertSeen, text)
			continue
		}
		recent++
	}
	if recent >= maxAlertsPerBurst {
		return false
	}
	h.alertSeen[message] = now
	return true
}

// journalState fills the declared keys with what the page and the prompt need:
// the newest match, its rules number, how many rows exist, and the last few
// rows of the match being played under the current rules.
func (h *Handlers) journalState(
	ctx context.Context,
	schemaRaw string,
) map[string]webmaster.JournalState {
	schema := webmaster.ParseJournalSchema(schemaRaw)
	if len(schema) == 0 {
		return nil
	}
	state := make(map[string]webmaster.JournalState, len(schema))
	for key, def := range schema {
		rules := def.Rules
		if rules <= 0 {
			rules = 1
		}
		site := webmaster.JournalState{Match: 1, Rules: rules}
		if latest, err := h.queries.JournalLatest(ctx, key); err == nil {
			if latest.CurrentMatch > 0 {
				site.Match = int(latest.CurrentMatch)
			}
			site.Rows = int(latest.RowCount)
		}
		if tail, err := h.queries.JournalTail(ctx, sqlc.JournalTailParams{
			Key:   key,
			Match: int64(site.Match),
			Rules: int64(rules),
			Limit: 3,
		}); err == nil {
			for i := len(tail) - 1; i >= 0; i-- {
				row := journalRowFrom(tail[i])
				site.Last = append(site.Last, webmaster.JournalTailRow{
					ID:      row.ID,
					Visitor: row.Visitor,
					Match:   int(row.Match),
					Payload: row.Payload,
				})
			}
		}
		state[key] = site
	}
	return state
}

// journalRulesUsed is the highest rules number stored under every key that
// has rows, declared or not. The schema tool reads it so a rules bump never
// lands on a number older rows already carry.
func (h *Handlers) journalRulesUsed(ctx context.Context) map[string]int {
	rows, err := h.queries.JournalRulesUsed(ctx)
	if err != nil {
		h.logger.Error("Failed to read journal rules", slog.Any("error", err))
		return nil
	}
	used := make(map[string]int, len(rows))
	for _, row := range rows {
		used[row.Key] = int(row.Rules)
	}
	return used
}
