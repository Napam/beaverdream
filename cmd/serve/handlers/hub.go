package handlers

import "sync"

// topic names one part of the read model. Commands publish the topics they
// changed; each open page's stream reads the database again for those parts
// and pushes what its page lacks. A topic carries no data, so a stream can
// never apply an event twice or out of order.
type topic uint8

const (
	topicPosts topic = 1 << iota
	topicSite
	topicBusy
	topicState
	topicJournal
	// topicTurns is a finished webmaster run, with or without a new version.
	// Only the admin panel reads it.
	topicTurns
	// topicDraft is the round the model is writing, many times a second.
	// It stays out of topicAll: only an admin stream asks for it.
	topicDraft

	topicAll = topicPosts | topicSite | topicBusy | topicState | topicJournal | topicTurns
)

// hub fans topics out to the open streams. Each subscriber keeps a bitmask of
// what changed since it last looked, so a slow stream coalesces a burst into
// one sync and the hub never blocks or drops.
type hub struct {
	mu   sync.Mutex
	subs map[*subscriber]struct{}
}

type subscriber struct {
	mu      sync.Mutex
	want    topic
	pending topic
	wake    chan struct{}
}

func newHub() *hub {
	return &hub{subs: make(map[*subscriber]struct{})}
}

// subscribe registers a stream for the topics in want. The caller must call
// the returned cancel.
func (h *hub) subscribe(want topic) (*subscriber, func()) {
	sub := &subscriber{want: want, wake: make(chan struct{}, 1)}
	h.mu.Lock()
	h.subs[sub] = struct{}{}
	h.mu.Unlock()
	return sub, func() {
		h.mu.Lock()
		delete(h.subs, sub)
		h.mu.Unlock()
	}
}

func (h *hub) publish(t topic) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for sub := range h.subs {
		if sub.want&t == 0 {
			continue
		}
		sub.mu.Lock()
		sub.pending |= t & sub.want
		sub.mu.Unlock()
		select {
		case sub.wake <- struct{}{}:
		default:
		}
	}
}

// take returns what changed since the last take and clears it.
func (s *subscriber) take() topic {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.pending
	s.pending = 0
	return t
}
