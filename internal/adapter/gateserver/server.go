// Package gateserver implements the Manager side of the Gate⇔Manager
// protocol (docs/protocol-gate-manager.md, architecture-manager.md 7節): a
// TCP+NDJSON server that Gate connects to as a client. It is a thin
// protocol adapter: every business decision is delegated to Application
// (application.ChallengeApplicationService); this package only
// parses/serializes NDJSON and tracks the one active connection.
//
// It also implements port.GateNotifier, since it's the only place that can
// actually send Gate something — application.Start/Load call back into it
// (RequestEvacuate/SendHardcoreReady/SendRejected) to carry out
// architecture-manager.md 8節's sequence.
package gateserver

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"

	"github.com/RayLight1732/hardcore-together-manager/internal/domain/challenge"
	"github.com/RayLight1732/hardcore-together-manager/internal/domain/records"
	"github.com/RayLight1732/hardcore-together-manager/internal/ndjson"
	"github.com/RayLight1732/hardcore-together-manager/internal/port"
)

var _ port.GateNotifier = (*Server)(nil)

// Application is what gateserver needs from
// application.ChallengeApplicationService. Every method takes requestID —
// the UUID Gate attached to the request being served — uniformly, whether
// or not that particular method happens to need it internally
// (docs/protocol-gate-manager.md 1節, architecture-manager.md 7節).
type Application interface {
	Snapshot(requestID string) challenge.State
	Start(ctx context.Context, requestID string, clean bool, requestedBy string) error
	Load(ctx context.Context, requestID string, name string, force bool, requestedBy string) error
	Deactivate(ctx context.Context, requestID string, requestedBy string) error
	SaveData(requestID string) ([]records.SaveDataEntry, error)
	Senpan(requestID string) ([]records.SenpanEntry, error)
}

// Server holds exactly one active Gate connection at a time (there is only
// ever one Gate instance in this deployment, architecture-manager.md 7節).
type Server struct {
	addr string
	app  Application

	mu      sync.Mutex
	current *ndjson.Conn

	// evacuateCompleteCh carries evacuate-complete arrivals through to
	// whichever RequestEvacuate call is currently waiting. See RequestEvacuate.
	evacuateCompleteCh chan struct{}
}

// NewServer builds a Server. Call SetApplication before Serve — it isn't
// taken as a constructor parameter because application.ChallengeApplicationService
// itself needs a *Server (as its port.GateNotifier) to be constructed
// first; main.go resolves this by building the Server, then the service,
// then wiring the service back in.
func NewServer(addr string) *Server {
	return &Server{addr: addr, evacuateCompleteCh: make(chan struct{}, 1)}
}

// SetApplication completes construction once the application layer has
// been built. See NewServer.
func (s *Server) SetApplication(app Application) {
	s.app = app
}

// Listen opens the listening socket, split from Serve so callers can learn
// the bound address (relevant when addr's port is 0, e.g. in tests) before
// Serve starts blocking.
func (s *Server) Listen() (net.Listener, error) {
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return nil, fmt.Errorf("gateserver: listen %s: %w", s.addr, err)
	}
	return ln, nil
}

// Serve accepts Gate connections on ln until ctx is cancelled.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	go func() {
		<-ctx.Done()
		ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("gateserver: accept: %w", err)
		}
		go s.handleConn(conn)
	}
}

// ListenAndServe is Listen followed by Serve.
func (s *Server) ListenAndServe(ctx context.Context) error {
	ln, err := s.Listen()
	if err != nil {
		return err
	}
	return s.Serve(ctx, ln)
}

// RequestEvacuate sends evacuate-request on the current Gate connection and
// blocks until evacuate-complete arrives or ctx is done
// (architecture-manager.md 8節 step 4). Any stale, previously-unconsumed
// evacuate-complete is discarded first so a late arrival from an
// already-abandoned cycle can't be mistaken for this one.
func (s *Server) RequestEvacuate(ctx context.Context, requestID, reason string) error {
	conn, err := s.currentConn()
	if err != nil {
		return err
	}

	select {
	case <-s.evacuateCompleteCh:
	default:
	}

	if err := conn.Send(evacuateRequestMsg{Type: "evacuate-request", RequestID: requestID, Reason: reason}); err != nil {
		return fmt.Errorf("gateserver: send evacuate-request: %w", err)
	}

	select {
	case <-s.evacuateCompleteCh:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// SendHardcoreReady notifies Gate that /start·/load has completed
// (architecture-manager.md 8節 step 9).
func (s *Server) SendHardcoreReady(requestID string) error {
	conn, err := s.currentConn()
	if err != nil {
		return err
	}
	return conn.Send(hardcoreReadyMsg{Type: "hardcore-ready", RequestID: requestID})
}

// SendRejected sends start-rejected, load-rejected, or deactivate-rejected
// (kind must be one of those strings) with reason.
func (s *Server) SendRejected(requestID, kind, reason string) error {
	conn, err := s.currentConn()
	if err != nil {
		return err
	}
	return conn.Send(rejectedMsg{Type: kind, RequestID: requestID, Reason: reason})
}

// SendDeactivateComplete notifies Gate that /deactivate's process stop has
// completed (docs/protocol-gate-manager.md 3.5a節).
func (s *Server) SendDeactivateComplete(requestID string) error {
	conn, err := s.currentConn()
	if err != nil {
		return err
	}
	return conn.Send(deactivateCompleteMsg{Type: "deactivate-complete", RequestID: requestID})
}

// SendFailed notifies Gate that an accepted start/load/deactivate failed
// partway through (docs/protocol-gate-manager.md 3.5b節).
func (s *Server) SendFailed(requestID, kind, reason string, recovered bool) error {
	conn, err := s.currentConn()
	if err != nil {
		return err
	}
	return conn.Send(failedMsg{Type: kind, RequestID: requestID, Reason: reason, Recovered: recovered})
}

func (s *Server) currentConn() (*ndjson.Conn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current == nil {
		return nil, errors.New("gateserver: no active Gate connection")
	}
	return s.current, nil
}

func (s *Server) adopt(conn *ndjson.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current != nil {
		s.current.Close()
	}
	s.current = conn
}

func (s *Server) release(conn *ndjson.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current == conn {
		s.current = nil
	}
}
