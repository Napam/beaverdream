package handlers

import (
	"log/slog"
	"testing"

	"github.com/Napam/beaverdream/lib/webmaster"
)

func attrMap(attrs []any) map[string]slog.Value {
	values := make(map[string]slog.Value, len(attrs))
	for _, attr := range attrs {
		a := attr.(slog.Attr)
		values[a.Key] = a.Value
	}
	return values
}

// A turn that spent its second round on done alone is the waste the finish
// log has to make visible.
func TestRoundAttrsSumTheRounds(t *testing.T) {
	got := attrMap(roundAttrs(webmaster.Result{
		DoneRound: 2,
		Rounds: []webmaster.RoundStats{
			{
				Round:        1,
				PromptTokens: 1000,
				CachedTokens: 600,
				Tools:        []string{"edit", "edit"},
				Rejected:     1,
			},
			{Round: 2, PromptTokens: 1000, CachedTokens: 1000, Tools: []string{"done"}},
		},
	}))

	if got["rounds"].Int64() != 2 || got["done_round"].Int64() != 2 {
		t.Errorf("rounds %v, done round %v", got["rounds"], got["done_round"])
	}
	if !got["last_round_done_only"].Bool() {
		t.Error("a done-only second round was not flagged")
	}
	if got["rejected"].Int64() != 1 || got["prompt_tokens"].Int64() != 2000 {
		t.Errorf("rejected %v, prompt tokens %v", got["rejected"], got["prompt_tokens"])
	}
	if got["cached_ratio"].Float64() != 0.8 {
		t.Errorf("cached ratio = %v, want 0.8", got["cached_ratio"])
	}

	single := attrMap(roundAttrs(webmaster.Result{
		DoneRound: 1,
		Rounds:    []webmaster.RoundStats{{Round: 1, Tools: []string{"done"}}},
	}))
	if single["last_round_done_only"].Bool() {
		t.Error("a one-round turn that only said something was flagged")
	}
	if single["cached_ratio"].Float64() != 0 {
		t.Errorf("cached ratio without tokens = %v", single["cached_ratio"])
	}
}
