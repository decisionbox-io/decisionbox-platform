package askserve

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/mongo"

	commonmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	applog "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
)

// shutdownGrace bounds how long graceful shutdown waits for in-flight turns to
// finalize after their contexts are cancelled, before the orphan sweep cleans
// up the rest. Turns can run for hours, so we never wait that long on SIGTERM —
// cancel, give finalize a moment, then let the next boot's sweep finish the job.
const shutdownGrace = 10 * time.Second

// Server is the always-up data-Q&A HTTP service. It owns the per-project pool,
// the durable turn store, and the concurrency budget; each accepted turn runs
// in its own background goroutine bounded by the wall-clock budget.
type Server struct {
	cfg   Config
	pool  *pool
	store *turnStore

	// global / per-project in-flight concurrency budget.
	globalSem chan struct{}
	projMu    sync.Mutex
	projSem   map[string]chan struct{}

	// in-flight turn lifecycle.
	turnsWG    sync.WaitGroup
	turnCtx    context.Context
	turnCancel context.CancelFunc
}

// NewServer builds the server from the serve-mode config, the project-runtime
// builder (injected by the agentserver, which owns provider wiring), and the
// Mongo database the turn store persists to.
func NewServer(cfg Config, build ProjectBuilder, db *mongo.Database) *Server {
	// turnCancel is stored on the server and invoked in shutdown() to cancel
	// every in-flight turn; its lifetime is the server's, not this function's.
	turnCtx, turnCancel := context.WithCancel(context.Background()) //nolint:gosec // cancelled in shutdown()
	return &Server{
		cfg:        cfg,
		pool:       newPool(build, cfg),
		store:      newTurnStore(db, cfg.TurnTTL),
		globalSem:  make(chan struct{}, cfg.MaxConcurrentTurns),
		projSem:    make(map[string]chan struct{}),
		turnCtx:    turnCtx,
		turnCancel: turnCancel,
	}
}

// Run starts the HTTP server and blocks until ctx is cancelled (SIGTERM), then
// drains in-flight turns and sweeps any stragglers to a terminal status.
func (s *Server) Run(ctx context.Context) error {
	if err := s.store.EnsureIndexes(ctx); err != nil {
		return fmt.Errorf("ask-serve: ensure indexes: %w", err)
	}
	// Boot-time orphan sweep: any turn left running by a previous process is
	// stranded — mark it failed with its partial transcript preserved.
	if n, err := s.store.SweepOrphans(ctx); err != nil {
		applog.WithError(err).Warn("ask-serve: boot orphan sweep failed")
	} else if n > 0 {
		applog.WithField("swept", n).Info("ask-serve: marked stranded turns failed at boot")
	}

	go s.pool.janitor()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /turns", s.handleStartTurn)
	mux.HandleFunc("GET /turns/{id}", s.handleGetTurn)
	mux.HandleFunc("GET /healthz", s.handleHealthz)

	srv := &http.Server{
		Addr:              ":" + strconv.Itoa(s.cfg.Port),
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		applog.WithField("port", s.cfg.Port).Info("ask-serve: listening")
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		s.shutdown(srv)
		return err
	case <-ctx.Done():
		applog.Info("ask-serve: shutdown signal received")
		s.shutdown(srv)
		return nil
	}
}

func (s *Server) shutdown(srv *http.Server) {
	// Stop accepting new connections first.
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutCtx)

	// Cancel in-flight turns and give finalize a brief grace window.
	s.turnCancel()
	done := make(chan struct{})
	go func() { s.turnsWG.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(shutdownGrace):
		applog.Warn("ask-serve: shutdown grace elapsed with turns still finalizing")
	}

	// Sweep anything that didn't finalize in time, then close pooled handles.
	sweepCtx, sweepCancel := context.WithTimeout(context.Background(), 10*time.Second)
	if n, err := s.store.SweepOrphans(sweepCtx); err != nil {
		applog.WithError(err).Warn("ask-serve: shutdown orphan sweep failed")
	} else if n > 0 {
		applog.WithField("swept", n).Info("ask-serve: swept turns on shutdown")
	}
	sweepCancel()
	s.pool.closeAll()
}

func (s *Server) handleStartTurn(w http.ResponseWriter, r *http.Request) {
	var req TurnRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if req.ProjectID == "" || req.TurnID == "" || req.Question == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "project_id, turn_id and question are required"})
		return
	}

	// Reserve concurrency before acknowledging. Excess load → 429 so the
	// caller can surface backpressure rather than queueing unboundedly.
	if !s.reserve(req.ProjectID) {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "ask-serve at capacity; retry shortly"})
		return
	}

	s.turnsWG.Add(1)
	go s.runTurn(req)

	writeJSON(w, http.StatusAccepted, TurnResponse{TurnID: req.TurnID, Status: commonmodels.AskTurnStatusRunning})
}

// runTurn executes one turn in the background, holding the concurrency slot for
// the turn's lifetime.
func (s *Server) runTurn(req TurnRequest) {
	defer s.turnsWG.Done()
	defer s.unreserve(req.ProjectID)

	ctx, cancel := context.WithTimeout(s.turnCtx, s.cfg.WallClock)
	defer cancel()

	// Ensure the turn record exists (idempotent; the API normally created it).
	ensureCtx, ensureCancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := s.store.EnsureTurn(ensureCtx, commonmodels.AskTurn{
		ID: req.TurnID, SessionID: req.SessionID, ProjectID: req.ProjectID, Question: req.Question,
	}); err != nil {
		applog.WithError(err).WithField("turn_id", req.TurnID).Error("ask-serve: ensure turn failed")
	}
	ensureCancel()

	rt, release, err := s.pool.acquire(ctx, req.ProjectID)
	if err != nil {
		run := &runner{cfg: s.cfg, store: s.store}
		st := &turnState{req: req}
		run.finishFailed(ctx, st, fmt.Sprintf("could not initialize the project's data connection: %v", err))
		return
	}
	defer release()

	(&runner{cfg: s.cfg, store: s.store}).run(ctx, rt, req)
}

func (s *Server) handleGetTurn(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	turn, err := s.store.GetTurn(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if turn == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "turn not found"})
		return
	}
	writeJSON(w, http.StatusOK, turn)
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// reserve takes a global + per-project concurrency slot, non-blocking.
func (s *Server) reserve(projectID string) bool {
	select {
	case s.globalSem <- struct{}{}:
	default:
		return false
	}
	ps := s.projectSem(projectID)
	select {
	case ps <- struct{}{}:
		return true
	default:
		<-s.globalSem // release the global slot we took
		return false
	}
}

func (s *Server) unreserve(projectID string) {
	<-s.projectSem(projectID)
	<-s.globalSem
}

func (s *Server) projectSem(projectID string) chan struct{} {
	s.projMu.Lock()
	defer s.projMu.Unlock()
	ps, ok := s.projSem[projectID]
	if !ok {
		ps = make(chan struct{}, s.cfg.MaxConcurrentPerProject)
		s.projSem[projectID] = ps
	}
	return ps
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
