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
	clean       bool
	requestedBy string
}

type loadCall struct {
	name        string
	force       bool
	requestedBy string
}

type deactivateCall struct {
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

func (f *fakeApplication) Snapshot() challenge.State { return f.snapshot }

func (f *fakeApplication) Start(ctx context.Context, clean bool, requestedBy string) error {
	f.mu.Lock()
	f.start = append(f.start, startCall{clean, requestedBy})
	f.mu.Unlock()
	f.called <- "start"
	return nil
}

func (f *fakeApplication) Load(ctx context.Context, name string, force bool, requestedBy string) error {
	f.mu.Lock()
	f.load = append(f.load, loadCall{name, force, requestedBy})
	f.mu.Unlock()
	f.called <- "load"
	return nil
}

func (f *fakeApplication) Deactivate(ctx context.Context, requestedBy string) error {
	f.mu.Lock()
	f.deactivate = append(f.deactivate, deactivateCall{requestedBy})
	f.mu.Unlock()
	f.called <- "deactivate"
	return nil
}

func (f *fakeApplication) SaveData() ([]records.SaveDataEntry, error) { return f.saveData, nil }
func (f *fakeApplication) Senpan() ([]records.SenpanEntry, error)     { return f.senpan, nil }

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

	if err := client.Send(struct {
		Type string `json:"type"`
	}{Type: "state-query"}); err != nil {
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
	if resp.Type != "state-response" || resp.State != "ready" || resp.Running != "true" {
		t.Fatalf("state-response = %+v, want {state-response ready true}", resp)
	}
}

func TestStart_DispatchesToApplication(t *testing.T) {
	_, app, dial := testServer(t)
	client := dial()

	if err := client.Send(startMsg{Type: "start", Clean: true, RequestedBy: "Steve"}); err != nil {
		t.Fatalf("send start: %v", err)
	}

	select {
	case <-app.called:
	case <-time.After(2 * time.Second):
		t.Fatal("application.Start was never called")
	}

	app.mu.Lock()
	defer app.mu.Unlock()
	if len(app.start) != 1 || app.start[0].clean != true || app.start[0].requestedBy != "Steve" {
		t.Fatalf("start calls = %+v, want [{true Steve}]", app.start)
	}
}

func TestDeactivate_DispatchesToApplication(t *testing.T) {
	_, app, dial := testServer(t)
	client := dial()

	if err := client.Send(deactivateMsg{Type: "deactivate", RequestedBy: "Steve"}); err != nil {
		t.Fatalf("send deactivate: %v", err)
	}

	select {
	case <-app.called:
	case <-time.After(2 * time.Second):
		t.Fatal("application.Deactivate was never called")
	}

	app.mu.Lock()
	defer app.mu.Unlock()
	if len(app.deactivate) != 1 || app.deactivate[0].requestedBy != "Steve" {
		t.Fatalf("deactivate calls = %+v, want [{Steve}]", app.deactivate)
	}
}

func TestSendDeactivateComplete(t *testing.T) {
	srv, _, dial := testServer(t)
	client := dial()
	waitForConnection(t, srv)

	if err := srv.SendDeactivateComplete(); err != nil {
		t.Fatalf("SendDeactivateComplete: %v", err)
	}

	raw, err := client.Receive()
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	typ, err := ndjson.Type(raw)
	if err != nil {
		t.Fatal(err)
	}
	if typ != "deactivate-complete" {
		t.Fatalf("type = %q, want deactivate-complete", typ)
	}
}

func TestLoad_DispatchesToApplication(t *testing.T) {
	_, app, dial := testServer(t)
	client := dial()

	if err := client.Send(loadMsg{Type: "load", Name: "latest", Force: false, RequestedBy: "Alex"}); err != nil {
		t.Fatalf("send load: %v", err)
	}

	select {
	case <-app.called:
	case <-time.After(2 * time.Second):
		t.Fatal("application.Load was never called")
	}

	app.mu.Lock()
	defer app.mu.Unlock()
	if len(app.load) != 1 || app.load[0].name != "latest" || app.load[0].requestedBy != "Alex" {
		t.Fatalf("load calls = %+v, want [{latest false Alex}]", app.load)
	}
}

func TestSavedataQuery_ReturnsConfiguredEntries(t *testing.T) {
	_, app, dial := testServer(t)
	app.saveData = []records.SaveDataEntry{
		{ChallengeID: "A", Event: records.Event{Type: records.EventClear}},
	}
	client := dial()

	if err := client.Send(struct {
		Type string `json:"type"`
	}{Type: "savedata-query"}); err != nil {
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
	if len(resp.Events) != 1 || resp.Events[0].ChallengeID != "A" {
		t.Fatalf("savedata-response = %+v", resp)
	}
}

func TestSenpanQuery_EchoesModeAndReturnsEntries(t *testing.T) {
	_, app, dial := testServer(t)
	app.senpan = []records.SenpanEntry{
		{Player: records.PlayerRef{UUID: "u1", Name: "Steve"}, Count: 3},
	}
	client := dial()

	if err := client.Send(senpanQueryMsg{Type: "senpan-query", Mode: "count"}); err != nil {
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
		evacDone <- srv.RequestEvacuate(ctx, "reset")
	}()

	raw, err := client.Receive()
	if err != nil {
		t.Fatalf("Receive evacuate-request: %v", err)
	}
	var msg evacuateRequestMsg
	if err := json.Unmarshal(raw, &msg); err != nil {
		t.Fatal(err)
	}
	if msg.Type != "evacuate-request" || msg.Reason != "reset" {
		t.Fatalf("evacuate-request = %+v", msg)
	}

	if err := client.Send(struct {
		Type string `json:"type"`
	}{Type: "evacuate-complete"}); err != nil {
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
	if err := srv.RequestEvacuate(ctx, "reset"); err == nil {
		t.Fatal("expected RequestEvacuate to time out")
	}
}

func TestRequestEvacuate_DiscardsStaleCompleteBeforeSending(t *testing.T) {
	srv, _, dial := testServer(t)
	client := dial()

	if err := client.Send(struct {
		Type string `json:"type"`
	}{Type: "evacuate-complete"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := srv.RequestEvacuate(ctx, "reset"); err == nil {
		t.Fatal("expected timeout: the stale evacuate-complete must not satisfy a fresh RequestEvacuate")
	}
}

func TestSendHardcoreReady(t *testing.T) {
	srv, _, dial := testServer(t)
	client := dial()
	waitForConnection(t, srv)

	if err := srv.SendHardcoreReady(); err != nil {
		t.Fatalf("SendHardcoreReady: %v", err)
	}

	raw, err := client.Receive()
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	typ, err := ndjson.Type(raw)
	if err != nil {
		t.Fatal(err)
	}
	if typ != "hardcore-ready" {
		t.Fatalf("type = %q, want hardcore-ready", typ)
	}
}

func TestSendRejected(t *testing.T) {
	srv, _, dial := testServer(t)
	client := dial()
	waitForConnection(t, srv)

	if err := srv.SendRejected("start-rejected", "挑戦が進行中です"); err != nil {
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
	if msg.Type != "start-rejected" || msg.Reason != "挑戦が進行中です" {
		t.Fatalf("rejectedMsg = %+v", msg)
	}
}

func TestNoActiveConnection_ReturnsError(t *testing.T) {
	srv, _, _ := testServer(t)

	if err := srv.SendHardcoreReady(); err == nil {
		t.Error("expected error with no active connection")
	}
	if err := srv.SendRejected("start-rejected", "x"); err == nil {
		t.Error("expected error with no active connection")
	}
	if err := srv.SendDeactivateComplete(); err == nil {
		t.Error("expected error with no active connection")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := srv.RequestEvacuate(ctx, "reset"); err == nil {
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
