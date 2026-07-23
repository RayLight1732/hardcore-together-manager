package gateserver

import (
	"context"
	"encoding/json"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/RayLight1732/hardcore-together-manager/internal/domain/challenge"
	"github.com/RayLight1732/hardcore-together-manager/internal/domain/records"
	"github.com/RayLight1732/hardcore-together-manager/internal/ndjson"
)

type startCall struct {
	requestID   string
	clean       bool
	requestedBy string
}

type loadCall struct {
	requestID   string
	name        string
	force       bool
	requestedBy string
}

type deactivateCall struct {
	requestID   string
	requestedBy string
}

type fakeApplication struct {
	mu sync.Mutex

	snapshot   challenge.State
	start      []startCall
	load       []loadCall
	deactivate []deactivateCall
	saveData   []records.SaveDataEntry
	senpan     []records.SenpanEntry

	called chan string
}

func newFakeApplication() *fakeApplication {
	return &fakeApplication{called: make(chan string, 8)}
}

func (f *fakeApplication) Snapshot(requestID string) challenge.State { return f.snapshot }

func (f *fakeApplication) Start(ctx context.Context, requestID string, clean bool, requestedBy string) error {
	f.mu.Lock()
	f.start = append(f.start, startCall{requestID, clean, requestedBy})
	f.mu.Unlock()
	f.called <- "start"
	return nil
}

func (f *fakeApplication) Load(ctx context.Context, requestID string, name string, force bool, requestedBy string) error {
	f.mu.Lock()
	f.load = append(f.load, loadCall{requestID, name, force, requestedBy})
	f.mu.Unlock()
	f.called <- "load"
	return nil
}

func (f *fakeApplication) Deactivate(ctx context.Context, requestID string, requestedBy string) error {
	f.mu.Lock()
	f.deactivate = append(f.deactivate, deactivateCall{requestID, requestedBy})
	f.mu.Unlock()
	f.called <- "deactivate"
	return nil
}

func (f *fakeApplication) SaveData(requestID string) ([]records.SaveDataEntry, error) {
	return f.saveData, nil
}
func (f *fakeApplication) Senpan(requestID string) ([]records.SenpanEntry, error) {
	return f.senpan, nil
}

func testServer(t *testing.T) (*Server, *fakeApplication, func() *ndjson.Conn) {
	t.Helper()

	app := newFakeApplication()
	srv := NewServer("127.0.0.1:0")
	srv.SetApplication(app)

	ln, err := srv.Listen()
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		if err := srv.Serve(ctx, ln); err != nil && ctx.Err() == nil {
			t.Error("Serve:", err)
		}
	}()

	addr := ln.Addr().String()
	dial := func() *ndjson.Conn {
		conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err != nil {
			t.Fatalf("dial %s: %v", addr, err)
		}
		t.Cleanup(func() { conn.Close() })
		return ndjson.NewConn(conn)
	}

	return srv, app, dial
}

func waitForConnection(t *testing.T, srv *Server) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := srv.currentConn(); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("server never adopted a connection")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestStateQuery_RespondsWithSnapshot(t *testing.T) {
	_, app, dial := testServer(t)
	app.snapshot = challenge.State{Phase: challenge.PhaseReady, Running: challenge.RunningTrue}
	client := dial()

	if err := client.Send(stateQueryMsg{Type: "state-query", RequestID: "req-1"}); err != nil {
		t.Fatalf("send state-query: %v", err)
	}

	raw, err := client.Receive()
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	var resp stateResponseMsg
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Type != "state-response" || resp.RequestID != "req-1" || resp.State != "ready" || resp.Running != "true" {
		t.Fatalf("state-response = %+v, want {state-response req-1 ready true}", resp)
	}
}

func TestStart_DispatchesToApplication(t *testing.T) {
	_, app, dial := testServer(t)
	client := dial()

	if err := client.Send(startMsg{Type: "start", RequestID: "req-1", Clean: true, RequestedBy: "Steve"}); err != nil {
		t.Fatalf("send start: %v", err)
	}

	select {
	case <-app.called:
	case <-time.After(2 * time.Second):
		t.Fatal("application.Start was never called")
	}

	app.mu.Lock()
	defer app.mu.Unlock()
	if len(app.start) != 1 || app.start[0].requestID != "req-1" || app.start[0].clean != true || app.start[0].requestedBy != "Steve" {
		t.Fatalf("start calls = %+v, want [{req-1 true Steve}]", app.start)
	}
}

func TestDeactivate_DispatchesToApplication(t *testing.T) {
	_, app, dial := testServer(t)
	client := dial()

	if err := client.Send(deactivateMsg{Type: "deactivate", RequestID: "req-1", RequestedBy: "Steve"}); err != nil {
		t.Fatalf("send deactivate: %v", err)
	}

	select {
	case <-app.called:
	case <-time.After(2 * time.Second):
		t.Fatal("application.Deactivate was never called")
	}

	app.mu.Lock()
	defer app.mu.Unlock()
	if len(app.deactivate) != 1 || app.deactivate[0].requestID != "req-1" || app.deactivate[0].requestedBy != "Steve" {
		t.Fatalf("deactivate calls = %+v, want [{req-1 Steve}]", app.deactivate)
	}
}

func TestSendDeactivateComplete(t *testing.T) {
	srv, _, dial := testServer(t)
	client := dial()
	waitForConnection(t, srv)

	if err := srv.SendDeactivateComplete("req-1"); err != nil {
		t.Fatalf("SendDeactivateComplete: %v", err)
	}

	raw, err := client.Receive()
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	var msg deactivateCompleteMsg
	if err := json.Unmarshal(raw, &msg); err != nil {
		t.Fatal(err)
	}
	if msg.Type != "deactivate-complete" || msg.RequestID != "req-1" {
		t.Fatalf("deactivateCompleteMsg = %+v", msg)
	}
}

func TestLoad_DispatchesToApplication(t *testing.T) {
	_, app, dial := testServer(t)
	client := dial()

	if err := client.Send(loadMsg{Type: "load", RequestID: "req-1", Name: "latest", Force: false, RequestedBy: "Alex"}); err != nil {
		t.Fatalf("send load: %v", err)
	}

	select {
	case <-app.called:
	case <-time.After(2 * time.Second):
		t.Fatal("application.Load was never called")
	}

	app.mu.Lock()
	defer app.mu.Unlock()
	if len(app.load) != 1 || app.load[0].requestID != "req-1" || app.load[0].name != "latest" || app.load[0].requestedBy != "Alex" {
		t.Fatalf("load calls = %+v, want [{req-1 latest false Alex}]", app.load)
	}
}

func TestSavedataQuery_ReturnsConfiguredEntries(t *testing.T) {
	_, app, dial := testServer(t)
	app.saveData = []records.SaveDataEntry{
		{ChallengeID: "A", Event: records.Event{Type: records.EventClear}},
	}
	client := dial()

	if err := client.Send(savedataQueryMsg{Type: "savedata-query", RequestID: "req-1"}); err != nil {
		t.Fatal(err)
	}

	raw, err := client.Receive()
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	var resp savedataResponseMsg
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.RequestID != "req-1" || len(resp.Events) != 1 || resp.Events[0].ChallengeID != "A" {
		t.Fatalf("savedata-response = %+v", resp)
	}
}

func TestSenpanQuery_EchoesModeAndReturnsEntries(t *testing.T) {
	_, app, dial := testServer(t)
	app.senpan = []records.SenpanEntry{
		{Player: records.PlayerRef{UUID: "u1", Name: "Steve"}, Count: 3},
	}
	client := dial()

	if err := client.Send(senpanQueryMsg{Type: "senpan-query", RequestID: "req-1", Mode: "count"}); err != nil {
		t.Fatal(err)
	}

	raw, err := client.Receive()
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	var resp senpanResponseMsg
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.RequestID != "req-1" {
		t.Errorf("RequestID = %q, want req-1", resp.RequestID)
	}
	if resp.Mode != "count" {
		t.Errorf("Mode = %q, want count", resp.Mode)
	}
	if len(resp.Entries) != 1 || resp.Entries[0].Count != 3 {
		t.Fatalf("senpan-response = %+v", resp)
	}
}

func TestRequestEvacuate_SendsAndWaitsForComplete(t *testing.T) {
	srv, _, dial := testServer(t)
	client := dial()
	waitForConnection(t, srv)

	evacDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		evacDone <- srv.RequestEvacuate(ctx, "req-1", "reset")
	}()

	raw, err := client.Receive()
	if err != nil {
		t.Fatalf("Receive evacuate-request: %v", err)
	}
	var msg evacuateRequestMsg
	if err := json.Unmarshal(raw, &msg); err != nil {
		t.Fatal(err)
	}
	if msg.Type != "evacuate-request" || msg.RequestID != "req-1" || msg.Reason != "reset" {
		t.Fatalf("evacuate-request = %+v", msg)
	}

	if err := client.Send(evacuateCompleteMsg{Type: "evacuate-complete", RequestID: "req-1"}); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-evacDone:
		if err != nil {
			t.Fatalf("RequestEvacuate: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RequestEvacuate never returned")
	}
}

func TestRequestEvacuate_TimesOutWithoutComplete(t *testing.T) {
	srv, _, dial := testServer(t)
	dial() // establish a connection but never send evacuate-complete

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := srv.RequestEvacuate(ctx, "req-1", "reset"); err == nil {
		t.Fatal("expected RequestEvacuate to time out")
	}
}

func TestRequestEvacuate_DiscardsStaleCompleteBeforeSending(t *testing.T) {
	srv, _, dial := testServer(t)
	client := dial()

	if err := client.Send(evacuateCompleteMsg{Type: "evacuate-complete", RequestID: "stale"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := srv.RequestEvacuate(ctx, "req-1", "reset"); err == nil {
		t.Fatal("expected timeout: the stale evacuate-complete must not satisfy a fresh RequestEvacuate")
	}
}

func TestSendHardcoreReady(t *testing.T) {
	srv, _, dial := testServer(t)
	client := dial()
	waitForConnection(t, srv)

	if err := srv.SendHardcoreReady("req-1"); err != nil {
		t.Fatalf("SendHardcoreReady: %v", err)
	}

	raw, err := client.Receive()
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	var msg hardcoreReadyMsg
	if err := json.Unmarshal(raw, &msg); err != nil {
		t.Fatal(err)
	}
	if msg.Type != "hardcore-ready" || msg.RequestID != "req-1" {
		t.Fatalf("hardcoreReadyMsg = %+v", msg)
	}
}

func TestSendRejected(t *testing.T) {
	srv, _, dial := testServer(t)
	client := dial()
	waitForConnection(t, srv)

	if err := srv.SendRejected("req-1", "start-rejected", "挑戦が進行中です"); err != nil {
		t.Fatalf("SendRejected: %v", err)
	}

	raw, err := client.Receive()
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	var msg rejectedMsg
	if err := json.Unmarshal(raw, &msg); err != nil {
		t.Fatal(err)
	}
	if msg.Type != "start-rejected" || msg.RequestID != "req-1" || msg.Reason != "挑戦が進行中です" {
		t.Fatalf("rejectedMsg = %+v", msg)
	}
}

func TestSendFailed(t *testing.T) {
	srv, _, dial := testServer(t)
	client := dial()
	waitForConnection(t, srv)

	if err := srv.SendFailed("req-1", "start-failed", "process exited before ready", true); err != nil {
		t.Fatalf("SendFailed: %v", err)
	}

	raw, err := client.Receive()
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	var msg failedMsg
	if err := json.Unmarshal(raw, &msg); err != nil {
		t.Fatal(err)
	}
	if msg.Type != "start-failed" || msg.RequestID != "req-1" || msg.Reason != "process exited before ready" || !msg.Recovered {
		t.Fatalf("failedMsg = %+v", msg)
	}
}

func TestNoActiveConnection_ReturnsError(t *testing.T) {
	srv, _, _ := testServer(t)

	if err := srv.SendHardcoreReady("req-1"); err == nil {
		t.Error("expected error with no active connection")
	}
	if err := srv.SendRejected("req-1", "start-rejected", "x"); err == nil {
		t.Error("expected error with no active connection")
	}
	if err := srv.SendDeactivateComplete("req-1"); err == nil {
		t.Error("expected error with no active connection")
	}
	if err := srv.SendFailed("req-1", "start-failed", "x", true); err == nil {
		t.Error("expected error with no active connection")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := srv.RequestEvacuate(ctx, "req-1", "reset"); err == nil {
		t.Error("expected error with no active connection")
	}
}

func TestNewConnection_ReplacesOldOne(t *testing.T) {
	_, _, dial := testServer(t)
	first := dial()
	second := dial()
	_ = second

	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := first.Receive(); err != nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("old Gate connection was never closed after a new one connected")
		}
	}
}
