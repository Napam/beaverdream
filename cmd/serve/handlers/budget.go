package handlers

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// Budget reports today's token use for the play session's identity and the
// configured limits. It never calls the model and never writes.
func (h *Handlers) Budget(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	if p == nil {
		writeStateError(w, http.StatusUnauthorized, "sign in to play")
		return
	}
	used, limit := int64(0), int64(0)
	if h.reg != nil {
		status, err := h.reg.BudgetStatus(r.Context(), p.IdentityID, h.budget)
		if err != nil {
			h.logger.Error("Failed to read the budget", slog.Any("error", err))
			writeStateError(w, http.StatusInternalServerError, "could not read the budget")
			return
		}
		used, limit = status.IdentityUsed, status.IdentityLimit
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(struct {
		Used  int64 `json:"used"`
		Limit int64 `json:"limit"`
	}{Used: used, Limit: limit})
}
