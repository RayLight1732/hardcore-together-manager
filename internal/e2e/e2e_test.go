// Package e2e drives a real cmd/manager binary as a subprocess, connecting
// to it over real TCP as both Gate and (indirectly, via cmd/fakehardcore)
// the hardcore MOD would. Unlike the rest of this codebase's tests — which
// exercise one layer at a time against fakes — this is the black-box check
// that everything wired together in cmd/manager actually behaves like
// specification.md says it should. It reproduces the manual verification
// done during development (architecture-manager.md's changelog), which is
// what originally caught the /load wipe-before-restore bug described there.
package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/RayLight1732/hardcore-together-manager/internal/ndjson"
)

func buildBinary(t *testing.T, outDir, outName, pkgDir string) string {
	t.Helper()
	out := filepath.Join(outDir, outName)
	cmd := exec.Command("go", "build", "-o", out, pkgDir)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build %s: %v\n%s", pkgDir, err, output)
	}
	return out
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func dialWithRetry(t *testing.T, addr string, timeout time.Duration) net.Conn {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			return conn
		}
		lastErr = err
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("dial %s: never became reachable: %v", addr, lastErr)
	return nil
}

func send(t *testing.T, conn *ndjson.Conn, v any) {
	t.Helper()
	if err := conn.Send(v); err != nil {
		t.Fatalf("send %+v: %v", v, err)
	}
}

func recv(t *testing.T, conn *ndjson.Conn) map[string]any {
	t.Helper()
	raw, err := conn.Receive()
	if err != nil {
		t.Fatalf("receive: %v", err)
	}
	var msg map[string]any
	if err := json.Unmarshal(raw, &msg); err != nil {
		t.Fatalf("unmarshal %s: %v", raw, err)
	}
	return msg
}

// doStartOrLoad sends req (a start or load message already accepted, i.e.
// not expected to be rejected) and drives the evacuate-request/complete
// handshake through to hardcore-ready (architecture-manager.md 8節).
func doStartOrLoad(t *testing.T, conn *ndjson.Conn, req any) {
	t.Helper()
	send(t, conn, req)

	msg := recv(t, conn)
	if msg["type"] == "evacuate-request" {
		send(t, conn, map[string]any{"type": "evacuate-complete"})
		msg = recv(t, conn)
	}
	if msg["type"] != "hardcore-ready" {
		t.Fatalf("expected hardcore-ready, got %+v", msg)
	}
}

func waitForNonEmptyDir(t *testing.T, dir string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		entries, err := os.ReadDir(dir)
		if err == nil && len(entries) > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never became non-empty (err=%v)", dir, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestE2E_StartArchiveLoadShutdown(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns real subprocesses and builds binaries; skipped in -short mode")
	}

	binDir := t.TempDir()
	managerBin := buildBinary(t, binDir, "manager", "../../cmd/manager")
	fakeBin := buildBinary(t, binDir, "fakehardcore", "../../cmd/fakehardcore")

	workDir := t.TempDir()
	hardcoreDir := filepath.Join(workDir, "hardcore")
	worldDir := filepath.Join(hardcoreDir, "world")
	archiveDir := filepath.Join(workDir, "archive")
	if err := os.MkdirAll(worldDir, 0o755); err != nil {
		t.Fatal(err)
	}
	propsPath := filepath.Join(hardcoreDir, "server.properties")
	if err := os.WriteFile(propsPath, []byte("difficulty=easy\nhardcore=false\nlevel-seed=\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	signalPort := freePort(t)
	gatePort := freePort(t)
	signalAddr := fmt.Sprintf("127.0.0.1:%d", signalPort)
	gateAddr := fmt.Sprintf("127.0.0.1:%d", gatePort)

	configYAML := fmt.Sprintf(`
signalPort: %d
gateListenAddr: %q
hardcore:
  workDir: "./hardcore"
  startCommand: [%q]
archive:
  dir: "./archive"
timeouts:
  evacuateCompleteSeconds: 5
  hardcoreReadySeconds: 10
  processStopSeconds: 5
`, signalPort, gateAddr, fakeBin)
	configPath := filepath.Join(workDir, "config.yml")
	if err := os.WriteFile(configPath, []byte(configYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(managerBin, "--config", "config.yml")
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(), "FAKEHARDCORE_SIGNAL_ADDR="+signalAddr)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		t.Fatalf("start manager: %v", err)
	}
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
		t.Logf("manager output:\n%s", output.String())
	})

	netConn := dialWithRetry(t, gateAddr, 5*time.Second)
	gateConn := ndjson.NewConn(netConn)
	defer gateConn.Close()

	t.Log("state-query before any /start: expect stopped/unknown")
	send(t, gateConn, map[string]any{"type": "state-query"})
	if msg := recv(t, gateConn); msg["state"] != "stopped" || msg["running"] != "unknown" {
		t.Fatalf("state-response = %+v, want {stopped unknown}", msg)
	}

	t.Log("/start without force: expect rejection (fresh Manager treats running as unknown)")
	send(t, gateConn, map[string]any{"type": "start", "force": false, "requestedBy": "e2e"})
	if msg := recv(t, gateConn); msg["type"] != "start-rejected" {
		t.Fatalf("expected start-rejected, got %+v", msg)
	}

	t.Log("/start force=true: expect evacuate-request -> hardcore-ready")
	doStartOrLoad(t, gateConn, map[string]any{"type": "start", "force": true, "requestedBy": "e2e"})

	send(t, gateConn, map[string]any{"type": "state-query"})
	if msg := recv(t, gateConn); msg["state"] != "ready" || msg["running"] != "true" {
		t.Fatalf("state-response after start = %+v, want {ready true}", msg)
	}

	props, err := os.ReadFile(propsPath)
	if err != nil {
		t.Fatalf("read server.properties: %v", err)
	}
	if !strings.Contains(string(props), "hardcore=true") {
		t.Fatalf("server.properties = %q, want hardcore=true enforced by Start", props)
	}

	t.Log("waiting for fakehardcore's automatic archive-request to land on disk")
	waitForNonEmptyDir(t, archiveDir, 5*time.Second)

	worldBefore, err := os.ReadFile(filepath.Join(worldDir, "level.dat"))
	if err != nil {
		t.Fatalf("read world marker before load: %v", err)
	}

	t.Log("/load latest force=true: expect evacuate-request -> hardcore-ready, restoring the archive")
	doStartOrLoad(t, gateConn, map[string]any{"type": "load", "name": "latest", "force": true, "requestedBy": "e2e"})

	send(t, gateConn, map[string]any{"type": "state-query"})
	if msg := recv(t, gateConn); msg["state"] != "ready" || msg["running"] != "true" {
		t.Fatalf("state-response after load = %+v, want {ready true}", msg)
	}

	worldAfter, err := os.ReadFile(filepath.Join(worldDir, "level.dat"))
	if err != nil {
		t.Fatalf("read world marker after load: %v", err)
	}
	// Regression guard for the wipe-before-restore bug (architecture-manager.md
	// 4節・8節changelog): if Load didn't wipe world/ first, archive.Restore's
	// os.CopyFS would either fail outright ("file exists") or, if it somehow
	// succeeded, the marker could still be the pre-load one by coincidence.
	// Comparing exact content confirms the archived world was actually copied
	// back, not left over from the previous run.
	if string(worldAfter) != string(worldBefore) {
		t.Fatalf("world marker after /load = %q, want restored content %q", worldAfter, worldBefore)
	}

	t.Log("SIGTERM: expect graceful shutdown, including the hardcore child process")
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal manager: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			if _, ok := err.(*exec.ExitError); !ok {
				t.Fatalf("manager exited with unexpected error: %v", err)
			}
		}
	case <-time.After(10 * time.Second):
		t.Fatal("manager did not exit within 10s of SIGTERM")
	}
	cmd.Process = nil // already reaped; skip the Cleanup's Kill/Wait

	if !strings.Contains(output.String(), "fakehardcore: got SIGTERM") {
		t.Error("expected the hardcore child process to have received SIGTERM too (graceful shutdown)")
	}
}
