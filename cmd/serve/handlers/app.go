package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/Napam/beaverdream/db/world/generated/sqlc"
	"github.com/Napam/beaverdream/lib/registry"
	"github.com/Napam/beaverdream/lib/webmaster"
	"github.com/Napam/beaverdream/lib/worlds"
	"github.com/go-chi/chi/v5"
)

// AppConfig is the instance-wide wiring. Config is copied into every world.
type AppConfig struct {
	Config           Config
	Budget           registry.Budget
	ModelConcurrency int
	WorldIdleTimeout time.Duration
}

// App owns the registry, the open worlds and the two planes. One App serves the
// app port and the play port from the same process.
type App struct {
	logger    *slog.Logger
	reg       *registry.Registry
	worlds    *worlds.Manager
	newRunner func() *webmaster.ChangesetRunner
	cfg       AppConfig
	sem       chan struct{}

	mu       sync.Mutex
	runtimes map[string]*worldRuntime
	loading  map[string]*worldLoad
	deleting map[string]bool
	closed   bool
	stop     chan struct{}
	wg       sync.WaitGroup
}

type worldRuntime struct {
	info      registry.World
	handle    *worlds.Handle
	h         *Handlers
	refs      int
	lastUsed  time.Time
	closing   bool
	drained   chan struct{}
	drainOnce sync.Once
}

type worldLoad struct {
	ready chan struct{}
	rt    *worldRuntime
	err   error
}

// NewApp wires the instance. The world files are not opened until a request
// needs one.
func NewApp(
	logger *slog.Logger,
	reg *registry.Registry,
	manager *worlds.Manager,
	newRunner func() *webmaster.ChangesetRunner,
	cfg AppConfig,
) *App {
	app := &App{
		logger:    logger,
		reg:       reg,
		worlds:    manager,
		newRunner: newRunner,
		cfg:       cfg,
		runtimes:  make(map[string]*worldRuntime),
		loading:   make(map[string]*worldLoad),
		deleting:  make(map[string]bool),
		stop:      make(chan struct{}),
	}
	if cfg.ModelConcurrency > 0 {
		app.sem = make(chan struct{}, cfg.ModelConcurrency)
	}
	if cfg.WorldIdleTimeout > 0 {
		app.wg.Add(1)
		go app.reapIdle()
	}
	return app
}

// Close stops every world worker and closes every handle.
func (a *App) Close() {
	a.mu.Lock()
	a.closed = true
	loads := make([]<-chan struct{}, 0, len(a.loading))
	for _, load := range a.loading {
		loads = append(loads, load.ready)
	}
	a.mu.Unlock()
	close(a.stop)
	a.wg.Wait()
	for _, ready := range loads {
		<-ready
	}

	a.mu.Lock()
	ids := make([]string, 0, len(a.runtimes))
	for id := range a.runtimes {
		ids = append(ids, id)
	}
	a.mu.Unlock()
	for _, id := range ids {
		a.evict(context.Background(), id)
	}
}

// reapIdle unloads a world after the configured quiet time. A world with an
// open stream, a queued post or a running turn is never idle.
func (a *App) reapIdle() {
	defer a.wg.Done()
	every := a.cfg.WorldIdleTimeout / 4
	if every < time.Second {
		every = time.Second
	}
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-a.stop:
			return
		case <-ticker.C:
		}
		var idle []*worldRuntime
		a.mu.Lock()
		for _, rt := range a.runtimes {
			if rt.refs > 0 || rt.closing {
				continue
			}
			if time.Since(rt.lastUsed) < a.cfg.WorldIdleTimeout {
				continue
			}
			if rt.h.hasWork() || rt.h.hub.subscriberCount() > 0 {
				continue
			}
			rt.closing = true
			rt.drained = make(chan struct{})
			idle = append(idle, rt)
		}
		a.mu.Unlock()
		for _, rt := range idle {
			a.closeRuntime(rt)
		}
	}
}

func (a *App) closeRuntime(rt *worldRuntime) {
	rt.h.Close()
	if err := rt.handle.Close(); err != nil {
		a.logger.Error("Failed to close an idle world",
			slog.String("world_id", rt.info.ID), slog.Any("error", err))
	}
	a.mu.Lock()
	if a.runtimes[rt.info.ID] == rt {
		delete(a.runtimes, rt.info.ID)
	}
	if rt.drained != nil {
		rt.drainOnce.Do(func() { close(rt.drained) })
	}
	a.mu.Unlock()
	a.logger.Info("World unloaded", slog.String("world_id", rt.info.ID))
}

// acquireWorld opens a world on first use and pins it for the request. The
// returned release must run when the request ends, including a live stream.
func (a *App) acquireWorld(
	ctx context.Context,
	info registry.World,
) (*worldRuntime, func(), error) {
	for {
		a.mu.Lock()
		if a.closed {
			a.mu.Unlock()
			return nil, nil, errors.New("shutting down")
		}
		if a.deleting[info.ID] {
			a.mu.Unlock()
			return nil, nil, registry.ErrWorldNotReady
		}
		if rt, ok := a.runtimes[info.ID]; ok {
			if rt.closing {
				done := rt.drained
				a.mu.Unlock()
				select {
				case <-done:
				case <-ctx.Done():
					return nil, nil, ctx.Err()
				}
				continue
			}
			rt.refs++
			rt.lastUsed = time.Now()
			a.mu.Unlock()
			return rt, func() { a.releaseWorld(rt) }, nil
		}
		if load, ok := a.loading[info.ID]; ok {
			ready := load.ready
			a.mu.Unlock()
			select {
			case <-ready:
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			}
			a.mu.Lock()
			err := load.err
			a.mu.Unlock()
			if err != nil {
				return nil, nil, err
			}
			continue
		}
		load := &worldLoad{ready: make(chan struct{})}
		a.loading[info.ID] = load
		a.mu.Unlock()

		rt, err := a.openWorld(ctx, info)
		if err == nil {
			// A delete may have moved the row while the file was opening.
			// Never cache a runtime for a world that is no longer ready.
			current, readErr := a.reg.GetWorld(ctx, info.ID)
			if readErr != nil || current.Status != registry.WorldReady {
				err = registry.ErrWorldNotReady
				if rt != nil {
					rt.h.Close()
					_ = rt.handle.Close()
				}
				rt = nil
			}
		}

		a.mu.Lock()
		if err == nil && (a.closed || a.deleting[info.ID]) {
			a.mu.Unlock()
			rt.h.Close()
			_ = rt.handle.Close()
			rt = nil
			err = registry.ErrWorldNotReady
			a.mu.Lock()
		}
		delete(a.loading, info.ID)
		if err == nil {
			rt.refs = 1
			rt.lastUsed = time.Now()
			a.runtimes[info.ID] = rt
		}
		load.rt, load.err = rt, err
		close(load.ready)
		a.mu.Unlock()
		if err != nil {
			return nil, nil, err
		}
		return rt, func() { a.releaseWorld(rt) }, nil
	}
}

func (a *App) openWorld(ctx context.Context, info registry.World) (*worldRuntime, error) {
	handle, err := a.worlds.Open(ctx, info.ID)
	if err != nil {
		return nil, err
	}
	queries := sqlc.New(handle.DB)
	h := New(Deps{
		Logger:   a.logger,
		Registry: a.reg,
		World:    info,
		DB:       handle.DB,
		Queries:  queries,
		Runner:   a.newRunner(),
		Config:   a.cfg.Config,
		Budget:   a.cfg.Budget,
		ModelSem: a.sem,
	})
	return &worldRuntime{info: info, handle: handle, h: h}, nil
}

func (a *App) releaseWorld(rt *worldRuntime) {
	a.mu.Lock()
	rt.refs--
	rt.lastUsed = time.Now()
	a.mu.Unlock()
}

// acquireBySlug reads the registry first, then opens the world file. The read
// is never inside a world write transaction.
func (a *App) acquireBySlug(ctx context.Context, slug string) (*worldRuntime, func(), error) {
	info, err := a.reg.GetWorldBySlug(ctx, slug)
	if err != nil {
		return nil, nil, err
	}
	if info.Status != registry.WorldReady {
		return nil, nil, registry.ErrWorldNotReady
	}
	return a.acquireWorld(ctx, *info)
}

// evict closes a world runtime after its requests and streams have drained.
// It is called before the registry row and files are deleted.
func (a *App) evict(ctx context.Context, worldID string) bool {
	a.mu.Lock()
	a.deleting[worldID] = true
	if load := a.loading[worldID]; load != nil {
		ready := load.ready
		a.mu.Unlock()
		select {
		case <-ready:
			return a.evict(ctx, worldID)
		case <-ctx.Done():
			return false
		}
	}
	rt, ok := a.runtimes[worldID]
	if ok {
		if rt.drained == nil {
			rt.drained = make(chan struct{})
		}
		if rt.closing {
			// An idle unload is already closing it. Wait for that close;
			// deleting the file under an open handle would race it.
			done := rt.drained
			a.mu.Unlock()
			select {
			case <-done:
				return true
			case <-ctx.Done():
				return false
			}
		}
		rt.closing = true
	}
	a.mu.Unlock()
	if !ok {
		return true
	}
	// The drain owns itself even if the request that started it is canceled.
	go func() {
		rt.h.hub.cancelAll()
		rt.h.stopWork()
		for {
			a.mu.Lock()
			refs := rt.refs
			a.mu.Unlock()
			if refs == 0 {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		a.closeRuntime(rt)
	}()
	select {
	case <-rt.drained:
		return true
	case <-ctx.Done():
		return false
	}
}

// worldRuntimeKey carries the pinned world through the request.
type worldRuntimeKey struct{}

func worldFrom(r *http.Request) *worldRuntime {
	rt, _ := r.Context().Value(worldRuntimeKey{}).(*worldRuntime)
	return rt
}

// withWorldSlug resolves the {slug} route parameter, pins the world and sets
// its handler. The release runs after the handler returns, so a stream keeps
// the world loaded for its whole life.
func (a *App) withWorldSlug(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slug := routeParam(r, "slug")
		rt, release, err := a.acquireBySlug(r.Context(), slug)
		if err != nil {
			a.worldMissing(w, r, err)
			return
		}
		defer release()
		ctx := context.WithValue(r.Context(), worldRuntimeKey{}, rt)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// worldMissing answers a play request for a world that is gone, not ready or
// never existed. It never recreates a file.
func (a *App) worldMissing(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, registry.ErrNotFound), errors.Is(err, worlds.ErrNotFound),
		errors.Is(err, registry.ErrWorldNotReady), errors.Is(err, worlds.ErrNotReady):
		http.NotFound(w, r)
	default:
		a.logger.Error("Failed to open a world", slog.Any("error", err))
		http.Error(w, "The world is not here.", http.StatusInternalServerError)
	}
}

// routeParam reads a chi URL parameter.
func routeParam(r *http.Request, name string) string {
	return chi.URLParam(r, name)
}
