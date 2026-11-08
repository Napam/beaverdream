package handlers

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Napam/beaverdream/db/world/generated/sqlc"
	"github.com/Napam/beaverdream/lib/webmaster"
	"github.com/go-chi/chi/v5"
)

// stateEntry is one item in a model-declared list.
type stateEntry struct {
	Author string `json:"author"`
	Value  string `json:"value"`
	At     string `json:"at"`
}

func parseStateSchema(raw string) map[string]webmaster.StateKey {
	return webmaster.ParseStateSchema(raw)
}

// State returns the declared schema and the current global values. Open pages
// get the values pushed as the $shared signal; this is the same read on
// demand. Visitor holdings are not in it: each page gets its own as $mine.
func (h *Handlers) State(w http.ResponseWriter, r *http.Request) {
	site, err := h.queries.CurrentSite(r.Context())
	if err != nil {
		http.Error(w, "unknown", http.StatusInternalServerError)
		return
	}
	schema := parseStateSchema(site.StateSchema)

	rows, err := h.queries.ListSharedState(r.Context())
	if err != nil {
		http.Error(w, "unknown", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	global := make(map[string]webmaster.StateKey, len(schema))
	for key, def := range schema {
		if def.Global() {
			global[key] = def
		}
	}
	_ = json.NewEncoder(w).Encode(struct {
		Schema map[string]webmaster.StateKey `json:"schema"`
		State  map[string]json.RawMessage    `json:"state"`
	}{Schema: global, State: stateValues(schema, rows)})
}

// stateValues fills every declared global key with a value of the shape its
// op expects. A stored value with the wrong shape is replaced by the default,
// so a schema change cannot serve a string where the page expects a list.
// Visitor counters and recipes are not shared values and are skipped.
func stateValues(
	schema map[string]webmaster.StateKey,
	rows []sqlc.SharedState,
) map[string]json.RawMessage {
	stored := make(map[string]string, len(rows))
	for _, row := range rows {
		stored[row.Key] = row.Value
	}

	values := make(map[string]json.RawMessage, len(schema))
	for key, def := range schema {
		if !def.Global() {
			continue
		}
		raw := stored[key]
		if !stateValueMatches(def, raw) {
			raw = stateDefault(def)
		}
		values[key] = json.RawMessage(raw)
	}
	return values
}

func stateDefault(def webmaster.StateKey) string {
	switch def.Op {
	case "counter":
		return "0"
	case "list":
		return "[]"
	case "choice":
		return "{}"
	default:
		return "null"
	}
}

func stateValueMatches(def webmaster.StateKey, raw string) bool {
	if raw == "" || !json.Valid([]byte(raw)) {
		return false
	}
	switch def.Op {
	case "counter":
		var count int64
		return json.Unmarshal([]byte(raw), &count) == nil
	case "list":
		var entries []stateEntry
		return json.Unmarshal([]byte(raw), &entries) == nil
	case "choice":
		var tally map[string]int
		return json.Unmarshal([]byte(raw), &tally) == nil
	default:
		return false
	}
}

// SetState applies one visitor action to a declared key or recipe. A global
// key takes its op; a visitor counter takes add=N or spend=N; a recipe
// trades. The value comes from the query string or a form field. A Datastar
// action gets 204, because every open page gets the new value from its live
// stream. A plain form is sent back to the world; a fetch gets JSON.
func (h *Handlers) SetState(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	if p == nil {
		if wantsHTML(r) {
			http.Redirect(w, r, h.worldPath(), http.StatusSeeOther)
			return
		}
		writeStateError(w, http.StatusUnauthorized, "sign in to play")
		return
	}
	author := p.DisplayName
	if err := r.ParseForm(); err != nil {
		writeStateError(w, http.StatusBadRequest, "bad form")
		return
	}

	key := chi.URLParam(r, "key")
	site, err := h.queries.CurrentSite(r.Context())
	if err != nil {
		writeStateError(w, http.StatusInternalServerError, "could not load the world")
		return
	}
	schema := parseStateSchema(site.StateSchema)
	def, ok := schema[key]
	if !ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		writeJSON(
			w,
			map[string]any{"ok": false, "reason": refusedGone, "error": "unknown state key"},
		)
		return
	}
	// The cooldown keys on the identity, not the display name, so two people
	// who picked the same name do not throttle each other. A visitor counter
	// is owned by the identity, the same id in every browser and world.
	visitor := p.IdentityID
	slot := visitor
	if !h.allowStateWrite(slot, key) {
		if def.Global() {
			if wantsHTML(r) {
				http.Redirect(w, r, h.worldPath(), http.StatusSeeOther)
				return
			}
			writeStateError(w, http.StatusTooManyRequests, "slow down")
			return
		}
		h.answerVisitorOp(w, r, nil, refusedSlow)
		return
	}
	if !def.Global() {
		h.visitorOp(w, r, key, def, schema, visitor, author)
		return
	}

	h.stateMu.Lock()
	existing := ""
	if row, err := h.queries.GetSharedState(r.Context(), key); err == nil {
		existing = row.Value
	}
	value, err := applyState(
		def,
		existing,
		webmaster.CleanStateValue(r.FormValue("value")),
		author,
		time.Now(),
	)
	if err != nil {
		h.stateMu.Unlock()
		writeStateError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := h.queries.UpsertSharedState(r.Context(), sqlc.UpsertSharedStateParams{
		Key:   key,
		Value: value,
	}); err != nil {
		h.stateMu.Unlock()
		h.logger.Error("Failed to store shared state", slog.Any("error", err))
		writeStateError(w, http.StatusInternalServerError, "could not store that")
		return
	}
	h.stateMu.Unlock()
	h.hub.publish(topicState)

	if isDatastar(r) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if wantsHTML(r) {
		http.Redirect(w, r, h.worldPath(), http.StatusSeeOther)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"key":   key,
		"value": json.RawMessage(value),
	})
}

// applyState turns one action into the stored JSON value. The ops are fixed,
// so the model can declare behavior without handing visitors arbitrary writes.
func applyState(
	def webmaster.StateKey,
	existing string,
	value string,
	author string,
	now time.Time,
) (string, error) {
	switch def.Op {
	case "counter":
		count := int64(0)
		if existing != "" {
			_ = json.Unmarshal([]byte(existing), &count)
		}
		return strconv.FormatInt(count+1, 10), nil

	case "list":
		if value == "" {
			return "", fmt.Errorf("value is required")
		}
		entries := make([]stateEntry, 0)
		if existing != "" {
			_ = json.Unmarshal([]byte(existing), &entries)
		}
		entries = append(entries, stateEntry{
			Author: webmaster.CleanStateValue(author),
			Value:  value,
			At:     now.UTC().Format(time.RFC3339),
		})
		limit := int(def.Max)
		if limit <= 0 {
			limit = 20
		}
		if len(entries) > limit {
			entries = entries[len(entries)-limit:]
		}
		data, err := json.Marshal(entries)
		if err != nil {
			return "", fmt.Errorf("could not store that")
		}
		return string(data), nil

	case "choice":
		if value == "" {
			return "", fmt.Errorf("value is required")
		}
		allowed := false
		for _, option := range def.Options {
			if option == value {
				allowed = true
				break
			}
		}
		if !allowed {
			return "", fmt.Errorf("not one of the options")
		}
		tally := make(map[string]int)
		if existing != "" {
			_ = json.Unmarshal([]byte(existing), &tally)
		}
		tally[value]++
		data, err := json.Marshal(tally)
		if err != nil {
			return "", fmt.Errorf("could not store that")
		}
		return string(data), nil
	}

	return "", fmt.Errorf("unknown state op")
}

// wantsHTML reports a plain browser form, which gets a redirect. Datastar
// also accepts text/html, but it would patch the redirected page into the
// document, so it never counts.
func wantsHTML(r *http.Request) bool {
	if isDatastar(r) {
		return false
	}
	return r.FormValue("redirect") != "" ||
		strings.Contains(r.Header.Get("Accept"), "text/html")
}

func writeJSON(w http.ResponseWriter, value any) {
	_ = json.NewEncoder(w).Encode(value)
}

func writeStateError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
