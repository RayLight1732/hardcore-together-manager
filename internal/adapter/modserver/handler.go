package modserver

import (
	"encoding/json"
	"errors"
	"net"

	domainarchive "github.com/RayLight1732/hardcore-together-manager/internal/domain/archive"
	"github.com/RayLight1732/hardcore-together-manager/internal/ndjson"
)

type readyMsg struct {
	Type    string `json:"type"`
	Running bool   `json:"running"`
}

type runningChangedMsg struct {
	Type    string `json:"type"`
	Running bool   `json:"running"`
}

type archiveRequestMsg struct {
	Type        string `json:"type"`
	Name        string `json:"name,omitempty"`
	ElapsedTime int64  `json:"elapsedTime"`
}

type archiveCompleteMsg struct {
	Type string `json:"type"`
	Name string `json:"name"`
}

// handleConn owns one MOD connection end to end: it becomes "the" current
// connection on accept, is read until disconnect, and on disconnect notifies
// Application (docs/protocol-mod-manager.md 5節) and forgets itself as
// current (architecture-manager.md 6節).
func (s *Server) handleConn(netConn net.Conn) {
	conn := ndjson.NewConn(netConn)
	s.adopt(conn)
	defer func() {
		conn.Close()
		s.release(conn)
		s.app.HandleDisconnect()
	}()

	for {
		raw, err := conn.Receive()
		if err != nil {
			return
		}
		s.dispatch(conn, raw)
	}
}

func (s *Server) dispatch(conn *ndjson.Conn, raw json.RawMessage) {
	typ, err := ndjson.Type(raw)
	if err != nil {
		s.logf("%v", err)
		return
	}

	switch typ {
	case "ready":
		var msg readyMsg
		if err := json.Unmarshal(raw, &msg); err != nil {
			s.logf("unmarshal ready: %v", err)
			return
		}
		s.app.HandleReady(msg.Running)
		select {
		case s.readyCh <- msg.Running:
		default:
			// Shouldn't normally happen (only one hardcore process/cycle at
			// a time), but don't block the read loop if it does: drop the
			// stale value in favor of the newest one.
			select {
			case <-s.readyCh:
			default:
			}
			s.readyCh <- msg.Running
		}

	case "running-changed":
		var msg runningChangedMsg
		if err := json.Unmarshal(raw, &msg); err != nil {
			s.logf("unmarshal running-changed: %v", err)
			return
		}
		s.app.HandleRunningChanged(msg.Running)

	case "archive-request":
		var msg archiveRequestMsg
		if err := json.Unmarshal(raw, &msg); err != nil {
			s.logf("unmarshal archive-request: %v", err)
			return
		}
		// Run off the read loop: HandleArchiveRequest can block on
		// application's opMutex for as long as a /start·/load sequence is
		// in flight, and the read loop must stay free to keep servicing
		// this connection meanwhile.
		go s.handleArchiveRequest(conn, msg)

	default:
		s.logf("unknown message type %q", typ)
	}
}

func (s *Server) handleArchiveRequest(conn *ndjson.Conn, msg archiveRequestMsg) {
	name, err := s.app.HandleArchiveRequest(msg.Name, msg.ElapsedTime)
	if err != nil {
		if errors.Is(err, domainarchive.ErrNameConflict) {
			// docs/protocol-mod-manager.md 7節: no immediate rejection signal
			// exists yet. The MOD detects failure via its own
			// archive-complete timeout.
			s.logf("archive-request for %q rejected: name already exists", msg.Name)
			return
		}
		s.logf("archive-request failed: %v", err)
		return
	}

	if err := conn.Send(archiveCompleteMsg{Type: "archive-complete", Name: name}); err != nil {
		s.logf("send archive-complete: %v", err)
	}
}
