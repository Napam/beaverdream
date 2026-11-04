package handlers

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Napam/beaverdream/db/generated/sqlc"
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
	schema := make(map[string]webmaster.StateKey)
	if strings.TrimSpace(raw) == "" {
		return schema
	}
	_ = json.Unmarshal([]byte(raw), &schema)
	return schema
}

// State returns the declared schema and the current values. Open pages get
// the values pushed as the $shared signal; this is the same read on demand.
func (h *Handlers) State(w http.ResponseWriter, r *http.Request) {
	site, err := h.queries.CurrentSite(r.Context())
	if err != nil {
		http.Error(w, "unknown", http.StatusInternalServerError)
		return
	}
	schema := parseStateSchema(site.StateSchema)

	rows, err := h.queries.ListSiteState(r.Context())
	if err != nil {
		http.Error(w, "unknown", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(struct {
		Schema map[string]webmaster.StateKey `json:"schema"`
		State  map[string]json.RawMessage    `json:"state"`
	}{Schema: schema, State: stateValues(schema, rows)})
}

// stateValues fills every declared key with a value of the shape its op
// expects. A stored value with the wrong shape is replaced by the default, so
// a schema change cannot serve a string where the page expects a list.
func stateValues(
	schema map[string]webmaster.StateKey,
	rows []sqlc.SiteState,
) map[string]json.RawMessage {
	stored := make(map[string]string, len(rows))
	for _, row := range rows {
		stored[row.Key] = row.Value
	}

	values := make(map[string]json.RawMessage, len(schema))
	for key, def := range schema {
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

// SetState applies one visitor action to a declared key. The value comes from
// the query string or a form field. A Datastar action gets 204, because every
// open page gets the new value from its live stream. A plain form is sent back
// to the board; a fetch gets JSON.
func (h *Handlers) SetState(w http.ResponseWriter, r *http.Request) {
	author := identityFromRequest(r)
	if author == "" {
		if wantsHTML(r) {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		writeStateError(w, http.StatusUnauthorized, "pick a name first")
		return
	}
	if err := r.ParseForm(); err != nil {
		writeStateError(w, http.StatusBadRequest, "bad form")
		return
	}

	key := chi.URLParam(r, "key")
	site, err := h.queries.CurrentSite(r.Context())
	if err != nil {
		writeStateError(w, http.StatusInternalServerError, "could not load the board")
		return
	}
	schema := parseStateSchema(site.StateSchema)
	def, ok := schema[key]
	if !ok {
		writeStateError(w, http.StatusNotFound, "unknown state key")
		return
	}
	if !h.allowStateWrite(author, key) {
		if wantsHTML(r) {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		writeStateError(w, http.StatusTooManyRequests, "slow down")
		return
	}

	h.stateMu.Lock()
	existing := ""
	if row, err := h.queries.GetSiteState(r.Context(), key); err == nil {
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
	if err := h.queries.UpsertSiteState(r.Context(), sqlc.UpsertSiteStateParams{
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
		http.Redirect(w, r, "/", http.StatusSeeOther)
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
		limit := def.Max
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

func writeStateError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
