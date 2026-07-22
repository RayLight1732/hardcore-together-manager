// Package modserver implements the Manager side of the MOD⇔Manager protocol
// (docs/protocol-mod-manager.md, architecture-manager.md 6節): a TCP+NDJSON
// server on 127.0.0.1:<signalPort> that the hardcore MOD connects to as a
// client. It is a thin protocol adapter: every business decision is
// delegated to Application (application.ChallengeApplicationService); this
// package only parses/serializes NDJSON and tracks the one active
// connection.
//
// It also implements port.ReadyWaiter, since it's the only place that knows
// when the hardcore MOD's `ready` signal for a just-launched process
// arrives — application.Start/Load block on WaitForReady while this
// adapter's dispatch loop is what eventually calls the matching Application
// method AND unblocks that wait.
package modserver

import (
	"context"
	"fmt"
	"log"
	"net"
	"sync"

	"github.com/RayLight1732/hardcore-together-manager/internal/ndjson"
	"github.com/RayLight1732/hardcore-together-manager/internal/port"
)

var _ port.ReadyWaiter = (*Server)(nil)

// Application is what modserver needs from
// application.ChallengeApplicationService.
type Application interface {
	HandleReady(running bool)
	HandleRunningChanged(running bool)
	HandleDisconnect()
	HandleArchiveRequest(name string, elapsedTime int64) (finalName string, err error)
}

// Server holds exactly one active MOD connection at a time: the one for the
// currently-running hardcore process (architecture-manager.md 6節).
type Server struct {
	addr string
	app  Application

	mu      sync.Mutex
	current *ndjson.Conn

	// readyCh carries the `running` value of the most recent `ready` signal
	// that application.Start/Load hasn't yet consumed (see WaitForReady).
	readyCh chan bool
}

// NewServer builds a Server. Call SetApplication before Serve — it isn't
// taken as a constructor parameter because application.ChallengeApplicationService
// itself needs a *Server (as its port.ReadyWaiter) to be constructed first;
// main.go resolves this by building the Server, then the service, then
// wiring the service back in.
func NewServer(addr string) *Server {
	return &Server{addr: addr, readyCh: make(chan bool, 1)}
}

// SetApplication completes construction once the application layer has
// been built. See NewServer.
func (s *Server) SetApplication(app Application) {
	s.app = app
}

// Listen opens the listening socket, split from Serve so callers (and
// tests) can learn the actual bound address before Serve starts blocking —
// useful when addr's port is 0.
func (s *Server) Listen() (net.Listener, error) {
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return nil, fmt.Errorf("modserver: listen %s: %w", s.addr, err)
	}
	return ln, nil
}

// Serve accepts MOD connections on ln until ctx is cancelled.
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
			return fmt.Errorf("modserver: accept: %w", err)
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

// WaitForReady blocks until the hardcore MOD's `ready` signal for the
// process application.Start/Load just launched arrives, or ctx is done
// (architecture-manager.md 8節 step 8). Call DrainReady immediately before
// starting the process this call is meant to wait for, so a late `ready`
// from a previous, already-timed-out cycle can't be mistaken for this one.
func (s *Server) WaitForReady(ctx context.Context) (running bool, err error) {
	select {
	case running := <-s.readyCh:
		return running, nil
	case <-ctx.Done():
		return false, ctx.Err()
	}
}

// DrainReady discards any unconsumed `ready` signal. See WaitForReady.
func (s *Server) DrainReady() {
	select {
	case <-s.readyCh:
	default:
	}
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

func (s *Server) logf(format string, args ...any) {
	log.Printf("modserver: "+format, args...)
}
