package modserver

import (
	"context"
	"encoding/json"
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
	_, _, dial := testServer(t)
	first := dial()
	if err := first.Send(readyMsg{Type: "ready", Running: true}); err != nil {
		t.Fatal(err)
	}

	second := dial()
	if err := second.Send(readyMsg{Type: "ready", Running: false}); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := first.Receive(); err != nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("old connection was never closed after a new one connected")
		}
	}
}

func TestArchiveRequest_Success(t *testing.T) {
	_, app, dial := testServer(t)
	app.archiveRequestResult = "save1"
	client := dial()

	if err := client.Send(archiveRequestMsg{Type: "archive-request", Name: "save1", ElapsedTime: 42}); err != nil {
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

	app.mu.Lock()
	defer app.mu.Unlock()
	if len(app.archiveRequestCalls) != 1 || app.archiveRequestCalls[0].elapsedTime != 42 {
		t.Errorf("archiveRequestCalls = %+v", app.archiveRequestCalls)
	}
}

func TestArchiveRequest_NameConflictSendsNoComplete(t *testing.T) {
	_, app, dial := testServer(t)
	app.archiveRequestErr = domainarchive.ErrNameConflict
	client := dial()

	if err := client.Send(archiveRequestMsg{Type: "archive-request", Name: "save1", ElapsedTime: 1}); err != nil {
		t.Fatal(err)
	}

	// No archive-complete should ever arrive (docs/protocol-mod-manager.md
	// 7節: no immediate rejection signal exists yet). Confirm silence for a
	// short window rather than genuinely waiting forever.
	recvDone := make(chan struct{})
	go func() {
		client.Receive() //nolint:errcheck // only used to detect any arrival at all
		close(recvDone)
	}()

	select {
	case <-recvDone:
		t.Fatal("received a message after a name-conflict archive-request; expected silence")
	case <-time.After(300 * time.Millisecond):
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
