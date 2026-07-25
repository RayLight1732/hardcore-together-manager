package modserver

import (
	"encoding/json"
	"errors"
	"fmt"
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
	RequestID   string `json:"requestId"`
	Name        string `json:"name,omitempty"`
	ElapsedTime int64  `json:"elapsedTime"`
}

type archiveCompleteMsg struct {
	Type      string `json:"type"`
	RequestID string `json:"requestId"`
	Name      string `json:"name"`
}

// archiveRejectedMsg answers an archive-request whose name collided with an
// existing archive (docs/protocol-mod-manager.md 3.5節). It's a one-shot
// immediate rejection, replacing the old silent-drop behavior that left the
// MOD blocked on its own 60s archive-complete timeout.
type archiveRejectedMsg struct {
	Type      string `json:"type"`
	RequestID string `json:"requestId"`
	Reason    string `json:"reason"`
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
			s.logf("archive-request for %q rejected: name already exists", msg.Name)
			reason := fmt.Sprintf("名前 %s は既に使用されています", msg.Name)
			rejected := archiveRejectedMsg{Type: "archive-rejected", RequestID: msg.RequestID, Reason: reason}
			if err := conn.Send(rejected); err != nil {
				s.logf("send archive-rejected: %v", err)
			}
			return
		}
		s.logf("archive-request failed: %v", err)
		return
	}

	complete := archiveCompleteMsg{Type: "archive-complete", RequestID: msg.RequestID, Name: name}
	if err := conn.Send(complete); err != nil {
		s.logf("send archive-complete: %v", err)
	}
}
