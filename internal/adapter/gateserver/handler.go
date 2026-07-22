package gateserver

import (
	"context"
	"encoding/json"
	"log"
	"net"

	"github.com/RayLight1732/hardcore-together-manager/internal/domain/records"
	"github.com/RayLight1732/hardcore-together-manager/internal/ndjson"
)

type stateResponseMsg struct {
	Type    string `json:"type"`
	State   string `json:"state"`
	Running string `json:"running"`
}

type hardcoreReadyMsg struct {
	Type string `json:"type"`
}

type startMsg struct {
	Type        string `json:"type"`
	Clean       bool   `json:"clean"`
	RequestedBy string `json:"requestedBy"`
}

type loadMsg struct {
	Type        string `json:"type"`
	Name        string `json:"name"`
	Force       bool   `json:"force"`
	RequestedBy string `json:"requestedBy"`
}

type deactivateMsg struct {
	Type        string `json:"type"`
	RequestedBy string `json:"requestedBy"`
}

type deactivateCompleteMsg struct {
	Type string `json:"type"`
}

// rejectedMsg's Type is "start-rejected", "load-rejected", or
// "deactivate-rejected" depending on which request it answers
// (docs/protocol-gate-manager.md 3.4節).
type rejectedMsg struct {
	Type   string `json:"type"`
	Reason string `json:"reason"`
}

type evacuateRequestMsg struct {
	Type   string `json:"type"`
	Reason string `json:"reason"`
}

type senpanQueryMsg struct {
	Type string `json:"type"`
	Mode string `json:"mode"`
}

type savedataResponseMsg struct {
	Type   string                  `json:"type"`
	Events []records.SaveDataEntry `json:"events"`
}

type senpanResponseMsg struct {
	Type    string                `json:"type"`
	Mode    string                `json:"mode"`
	Entries []records.SenpanEntry `json:"entries"`
}

// handleConn owns one Gate connection end to end: adopted as current on
// accept, read until disconnect, then forgotten (architecture-manager.md
// 7節). Unlike modserver, disconnecting a Gate connection doesn't mutate
// hardcore's state — Gate itself is what treats "no connection" as unknown
// state on its side (spec 2.1節).
func (s *Server) handleConn(netConn net.Conn) {
	conn := ndjson.NewConn(netConn)
	s.adopt(conn)
	defer func() {
		conn.Close()
		s.release(conn)
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
	case "state-query":
		snap := s.app.Snapshot()
		resp := stateResponseMsg{Type: "state-response", State: string(snap.Phase), Running: string(snap.Running)}
		if err := conn.Send(resp); err != nil {
			s.logf("send state-response: %v", err)
		}

	case "start":
		var msg startMsg
		if err := json.Unmarshal(raw, &msg); err != nil {
			s.logf("unmarshal start: %v", err)
			return
		}
		// Off the read loop: Start can send evacuate-request and block
		// waiting for evacuate-complete on this same connection, which the
		// read loop must stay free to deliver (architecture-manager.md 7節・8節).
		go func() {
			if err := s.app.Start(context.Background(), msg.Clean, msg.RequestedBy); err != nil {
				s.logf("start: %v", err)
			}
		}()

	case "load":
		var msg loadMsg
		if err := json.Unmarshal(raw, &msg); err != nil {
			s.logf("unmarshal load: %v", err)
			return
		}
		go func() {
			if err := s.app.Load(context.Background(), msg.Name, msg.Force, msg.RequestedBy); err != nil {
				s.logf("load: %v", err)
			}
		}()

	case "deactivate":
		var msg deactivateMsg
		if err := json.Unmarshal(raw, &msg); err != nil {
			s.logf("unmarshal deactivate: %v", err)
			return
		}
		go func() {
			if err := s.app.Deactivate(context.Background(), msg.RequestedBy); err != nil {
				s.logf("deactivate: %v", err)
			}
		}()

	case "evacuate-complete":
		select {
		case s.evacuateCompleteCh <- struct{}{}:
		default:
		}

	case "savedata-query":
		entries, err := s.app.SaveData()
		if err != nil {
			s.logf("savedata: %v", err)
			entries = []records.SaveDataEntry{}
		}
		if err := conn.Send(savedataResponseMsg{Type: "savedata-response", Events: entries}); err != nil {
			s.logf("send savedata-response: %v", err)
		}

	case "senpan-query":
		var msg senpanQueryMsg
		if err := json.Unmarshal(raw, &msg); err != nil {
			s.logf("unmarshal senpan-query: %v", err)
			return
		}
		entries, err := s.app.Senpan()
		if err != nil {
			s.logf("senpan: %v", err)
			entries = []records.SenpanEntry{}
		}
		resp := senpanResponseMsg{Type: "senpan-response", Mode: msg.Mode, Entries: entries}
		if err := conn.Send(resp); err != nil {
			s.logf("send senpan-response: %v", err)
		}

	default:
		s.logf("unknown message type %q", typ)
	}
}

func (s *Server) logf(format string, args ...any) {
	log.Printf("gateserver: "+format, args...)
}
