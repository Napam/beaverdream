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
	mu     sync.Mutex
	subs   map[*subscriber]struct{}
	closed bool
}

type subscriber struct {
	mu      sync.Mutex
	want    topic
	pending topic
	wake    chan struct{}
	// closed ends a stream whose identity or session was revoked, or whose
	// world is shutting down. It is separate from wake so a cancellation is
	// never mistaken for a topic.
	closed      chan struct{}
	closeOnce   sync.Once
	identityID  string
	sessionHash string
}

func newHub() *hub {
	return &hub{subs: make(map[*subscriber]struct{})}
}

// subscribe registers a stream for the topics in want. The caller must call
// the returned cancel.
func (h *hub) subscribe(want topic) (*subscriber, func()) {
	return h.subscribeAs(want, "", "")
}

// subscribeAs registers a stream and remembers which identity and session it
// belongs to, so a revocation can close exactly the affected pages. A hub that
// has shut down stays closed: the stream gets an already-closed subscription
// instead of joining a hub that will never publish again.
func (h *hub) subscribeAs(want topic, identityID, sessionHash string) (*subscriber, func()) {
	sub := &subscriber{
		want:        want,
		wake:        make(chan struct{}, 1),
		closed:      make(chan struct{}),
		identityID:  identityID,
		sessionHash: sessionHash,
	}
	h.mu.Lock()
	if h.closed {
		sub.cancel()
	} else {
		h.subs[sub] = struct{}{}
	}
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

func (s *subscriber) cancel() {
	s.closeOnce.Do(func() { close(s.closed) })
}

// cancelIdentity closes every stream held by one identity. Disabling an
// identity runs it on every loaded world's hub, so no tab keeps receiving
// pushes after its sessions are gone.
func (h *hub) cancelIdentity(identityID string) {
	if identityID == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for sub := range h.subs {
		if sub.identityID == identityID {
			sub.cancel()
		}
	}
}

// cancelSession closes the streams of one session token hash.
func (h *hub) cancelSession(sessionHash string) {
	if sessionHash == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for sub := range h.subs {
		if sub.sessionHash == sessionHash {
			sub.cancel()
		}
	}
}

// cancelAll closes every stream, so a world can be closed without racing one.
// It leaves the hub terminal: a stream that reaches subscribeAs after this
// point is refused, so a request already pinned by the world middleware cannot
// join after the drain started and keep the runtime alive forever.
func (h *hub) cancelAll() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	for sub := range h.subs {
		sub.cancel()
	}
}

// subscriberCount reports the open streams, for idle unload.
func (h *hub) subscriberCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}
