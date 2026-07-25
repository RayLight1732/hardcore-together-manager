package modserver

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	domainarchive "github.com/RayLight1732/hardcore-together-manager/internal/domain/archive"
	"github.com/RayLight1732/hardcore-together-manager/internal/ndjson"
)

type archiveRequestCall struct {
	name        string
	elapsedTime int64
}

type fakeApplication struct {
	mu sync.Mutex

	readyCalls           []bool
	runningChangedCalls  []bool
	disconnectCalls      int
	archiveRequestCalls  []archiveRequestCall
	archiveRequestResult string
	archiveRequestErr    error
}

func (f *fakeApplication) HandleReady(running bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.readyCalls = append(f.readyCalls, running)
}

func (f *fakeApplication) HandleRunningChanged(running bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runningChangedCalls = append(f.runningChangedCalls, running)
}

func (f *fakeApplication) HandleDisconnect() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.disconnectCalls++
}

func (f *fakeApplication) HandleArchiveRequest(name string, elapsedTime int64) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.archiveRequestCalls = append(f.archiveRequestCalls, archiveRequestCall{name, elapsedTime})
	if f.archiveRequestErr != nil {
		return "", f.archiveRequestErr
	}
	if f.archiveRequestResult != "" {
		return f.archiveRequestResult, nil
	}
	return name, nil
}

// testServer starts a Server on an ephemeral loopback port and returns it
// along with a dialer for connecting fake MOD clients.
func testServer(t *testing.T) (*Server, *fakeApplication, func() *ndjson.Conn) {
	t.Helper()

	app := &fakeApplication{}
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

func waitForCalls(t *testing.T, timeout time.Duration, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if check() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("condition never became true")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestReady_CallsApplicationAndUnblocksWaitForReady(t *testing.T) {
	srv, app, dial := testServer(t)
	client := dial()

	waitDone := make(chan struct {
		running bool
		err     error
	}, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		running, err := srv.WaitForReady(ctx)
		waitDone <- struct {
			running bool
			err     error
		}{running, err}
	}()

	if err := client.Send(readyMsg{Type: "ready", Running: true}); err != nil {
		t.Fatalf("send ready: %v", err)
	}

	select {
	case res := <-waitDone:
		if res.err != nil {
			t.Fatalf("WaitForReady error: %v", res.err)
		}
		if !res.running {
			t.Error("WaitForReady running = false, want true")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WaitForReady never returned")
	}

	waitForCalls(t, 2*time.Second, func() bool {
		app.mu.Lock()
		defer app.mu.Unlock()
		return len(app.readyCalls) == 1 && app.readyCalls[0] == true
	})
}

func TestRunningChanged_CallsApplication(t *testing.T) {
	_, app, dial := testServer(t)
	client := dial()

	if err := client.Send(runningChangedMsg{Type: "running-changed", Running: false}); err != nil {
		t.Fatal(err)
	}
	waitForCalls(t, 2*time.Second, func() bool {
		app.mu.Lock()
		defer app.mu.Unlock()
		return len(app.runningChangedCalls) == 1 && app.runningChangedCalls[0] == false
	})
}

func TestDisconnect_CallsApplication(t *testing.T) {
	_, app, dial := testServer(t)
	client := dial()
	if err := client.Send(readyMsg{Type: "ready", Running: true}); err != nil {
		t.Fatal(err)
	}
	client.Close()

	waitForCalls(t, 2*time.Second, func() bool {
		app.mu.Lock()
		defer app.mu.Unlock()
		return app.disconnectCalls == 1
	})
}

func TestNewConnection_ReplacesOldOne(t *testing.T) {
	// first.Receive() blocks on the socket with no deadline of its own, so
	// it must get an explicit read deadline here — otherwise a bug that
	// makes the server fail to close the old connection hangs this test for
	// the full 10-minute test-binary timeout instead of failing in ~2s.
	_, _, dial := testServer(t)
	first := dial()
	if err := first.Send(readyMsg{Type: "ready", Running: true}); err != nil {
		t.Fatal(err)
	}

	second := dial()
	if err := second.Send(readyMsg{Type: "ready", Running: false}); err != nil {
		t.Fatal(err)
	}

	if err := first.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	_, err := first.Receive()
	if err == nil {
		t.Fatal("expected the old connection's Receive to return an error once replaced")
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		t.Fatal("old connection was never closed after a new one connected")
	}
}

func TestArchiveRequest_Success(t *testing.T) {
	_, app, dial := testServer(t)
	app.archiveRequestResult = "save1"
	client := dial()

	if err := client.Send(archiveRequestMsg{Type: "archive-request", RequestID: "req-1", Name: "save1", ElapsedTime: 42}); err != nil {
		t.Fatalf("send archive-request: %v", err)
	}

	raw, err := client.Receive()
	if err != nil {
		t.Fatalf("Receive archive-complete: %v", err)
	}
	typ, err := ndjson.Type(raw)
	if err != nil {
		t.Fatal(err)
	}
	if typ != "archive-complete" {
		t.Fatalf("type = %q, want archive-complete", typ)
	}
	var msg archiveCompleteMsg
	if err := json.Unmarshal(raw, &msg); err != nil {
		t.Fatal(err)
	}
	if msg.Name != "save1" {
		t.Errorf("archive-complete name = %q, want save1", msg.Name)
	}
	if msg.RequestID != "req-1" {
		t.Errorf("archive-complete requestId = %q, want req-1", msg.RequestID)
	}

	app.mu.Lock()
	defer app.mu.Unlock()
	if len(app.archiveRequestCalls) != 1 || app.archiveRequestCalls[0].elapsedTime != 42 {
		t.Errorf("archiveRequestCalls = %+v", app.archiveRequestCalls)
	}
}

func TestArchiveRequest_NameConflictSendsArchiveRejected(t *testing.T) {
	_, app, dial := testServer(t)
	app.archiveRequestErr = domainarchive.ErrNameConflict
	client := dial()

	if err := client.Send(archiveRequestMsg{Type: "archive-request", RequestID: "req-2", Name: "save1", ElapsedTime: 1}); err != nil {
		t.Fatal(err)
	}

	raw, err := client.Receive()
	if err != nil {
		t.Fatalf("Receive archive-rejected: %v", err)
	}
	typ, err := ndjson.Type(raw)
	if err != nil {
		t.Fatal(err)
	}
	if typ != "archive-rejected" {
		t.Fatalf("type = %q, want archive-rejected", typ)
	}
	var msg archiveRejectedMsg
	if err := json.Unmarshal(raw, &msg); err != nil {
		t.Fatal(err)
	}
	if msg.RequestID != "req-2" {
		t.Errorf("archive-rejected requestId = %q, want req-2", msg.RequestID)
	}
	if msg.Reason == "" {
		t.Error("archive-rejected reason is empty, want a human-readable reason")
	}
}

func TestArchiveRequest_GenericFailureSendsArchiveRejected(t *testing.T) {
	// Regression guard: archive-rejected must not be name-conflict-only.
	// Any HandleArchiveRequest failure (e.g. the world copy itself failing)
	// must reach the MOD immediately rather than being silently logged and
	// leaving the MOD to fall back on its own 60s archive-complete timeout
	// (architecture-manager.md 4節「検討の上、見送り」の撤回）。
	_, app, dial := testServer(t)
	app.archiveRequestErr = errors.New("fsarchive: copy world: open data/random_sequences.dat: no such file or directory")
	client := dial()

	if err := client.Send(archiveRequestMsg{Type: "archive-request", RequestID: "req-3", Name: "", ElapsedTime: 1}); err != nil {
		t.Fatal(err)
	}

	raw, err := client.Receive()
	if err != nil {
		t.Fatalf("Receive archive-rejected: %v", err)
	}
	typ, err := ndjson.Type(raw)
	if err != nil {
		t.Fatal(err)
	}
	if typ != "archive-rejected" {
		t.Fatalf("type = %q, want archive-rejected", typ)
	}
	var msg archiveRejectedMsg
	if err := json.Unmarshal(raw, &msg); err != nil {
		t.Fatal(err)
	}
	if msg.RequestID != "req-3" {
		t.Errorf("archive-rejected requestId = %q, want req-3", msg.RequestID)
	}
	if msg.Reason == "" {
		t.Error("archive-rejected reason is empty, want a human-readable reason")
	}
}

func TestDrainReady_DiscardsStaleValue(t *testing.T) {
	srv, _, dial := testServer(t)
	client := dial()

	if err := client.Send(readyMsg{Type: "ready", Running: true}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)

	srv.DrainReady()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := srv.WaitForReady(ctx); err == nil {
		t.Fatal("expected WaitForReady to time out after DrainReady discarded the stale value")
	}
}
