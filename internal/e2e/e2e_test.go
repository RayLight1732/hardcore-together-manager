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
	"strconv"
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

// doStartOrLoad sends req (a start/load/deactivate message already
// accepted, i.e. not expected to be rejected) and drives the
// evacuate-request/complete handshake through to the given terminal
// message type (architecture-manager.md 8節・8a節).
func doStartOrLoad(t *testing.T, conn *ndjson.Conn, req any, terminal string) {
	t.Helper()
	send(t, conn, req)

	msg := recv(t, conn)
	if msg["type"] == "evacuate-request" {
		send(t, conn, map[string]any{"type": "evacuate-complete"})
		msg = recv(t, conn)
	}
	if msg["type"] != terminal {
		t.Fatalf("expected %s, got %+v", terminal, msg)
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

// managerLayout is the on-disk shape (config.yml + hardcore/) one manager
// binary invocation operates on, factored out so the restart/orphan
// regression tests can reuse the exact same workDir across two separate
// manager runs.
type managerLayout struct {
	workDir     string
	hardcoreDir string
	worldDir    string
	archiveDir  string
	statePath   string
	pidFilePath string
	propsPath   string
	signalAddr  string
	gateAddr    string
	managerBin  string
	fakeBin     string
}

func newManagerLayout(t *testing.T, binDir string) *managerLayout {
	t.Helper()

	workDir := t.TempDir()
	hardcoreDir := filepath.Join(workDir, "hardcore")
	if err := os.MkdirAll(hardcoreDir, 0o755); err != nil {
		t.Fatal(err)
	}
	propsPath := filepath.Join(hardcoreDir, "server.properties")
	if err := os.WriteFile(propsPath, []byte("difficulty=easy\nhardcore=false\nlevel-seed=\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	signalPort := freePort(t)
	gatePort := freePort(t)

	l := &managerLayout{
		workDir:     workDir,
		hardcoreDir: hardcoreDir,
		worldDir:    filepath.Join(hardcoreDir, "world"), // intentionally not created: a fresh deploy has no world/ yet
		archiveDir:  filepath.Join(workDir, "archive"),
		statePath:   filepath.Join(workDir, "state.json"),
		pidFilePath: filepath.Join(workDir, "hardcore.pid"),
		propsPath:   propsPath,
		signalAddr:  fmt.Sprintf("127.0.0.1:%d", signalPort),
		gateAddr:    fmt.Sprintf("127.0.0.1:%d", gatePort),
		managerBin:  buildBinary(t, binDir, "manager", "../../cmd/manager"),
		fakeBin:     buildBinary(t, binDir, "fakehardcore", "../../cmd/fakehardcore"),
	}

	configYAML := fmt.Sprintf(`
signalPort: %d
gateListenAddr: %q
state:
  path: "./state.json"
hardcore:
  workDir: "./hardcore"
  startCommand: [%q]
  pidFile: "./hardcore.pid"
archive:
  dir: "./archive"
timeouts:
  evacuateCompleteSeconds: 5
  hardcoreReadySeconds: 10
  processStopSeconds: 5
`, signalPort, l.gateAddr, l.fakeBin)
	if err := os.WriteFile(filepath.Join(workDir, "config.yml"), []byte(configYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	return l
}

// managerProc is one running cmd/manager subprocess against a
// managerLayout.
type managerProc struct {
	cmd    *exec.Cmd
	output *bytes.Buffer
}

// start launches cmd/manager against l, cleaning it up (forcefully) at test
// end unless the test has already reaped it itself (cmd.Process set to nil,
// mirroring the pattern the original single-run test used for its own
// graceful-shutdown check).
func (l *managerLayout) start(t *testing.T) *managerProc {
	t.Helper()

	cmd := exec.Command(l.managerBin, "--config", "config.yml")
	cmd.Dir = l.workDir
	cmd.Env = append(os.Environ(), "FAKEHARDCORE_SIGNAL_ADDR="+l.signalAddr)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		t.Fatalf("start manager: %v", err)
	}
	p := &managerProc{cmd: cmd, output: &output}
	t.Cleanup(func() {
		if p.cmd.Process != nil {
			_ = p.cmd.Process.Kill()
			_ = p.cmd.Wait()
		}
		t.Logf("manager output:\n%s", output.String())
	})
	return p
}

func (l *managerLayout) dialGate(t *testing.T) *ndjson.Conn {
	t.Helper()
	netConn := dialWithRetry(t, l.gateAddr, 5*time.Second)
	conn := ndjson.NewConn(netConn)
	t.Cleanup(func() { conn.Close() })
	return conn
}

func TestE2E_StartArchiveLoadShutdown(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns real subprocesses and builds binaries; skipped in -short mode")
	}

	binDir := t.TempDir()
	l := newManagerLayout(t, binDir)
	proc := l.start(t)
	gateConn := l.dialGate(t)

	t.Log("state-query before any /start: expect stopped/false (a fresh deploy, not unknown)")
	send(t, gateConn, map[string]any{"type": "state-query"})
	if msg := recv(t, gateConn); msg["state"] != "stopped" || msg["running"] != "false" {
		t.Fatalf("state-response = %+v, want {stopped false}", msg)
	}

	t.Log("/start (clean:false) with no world/ yet: expect rejection")
	send(t, gateConn, map[string]any{"type": "start", "clean": false, "requestedBy": "e2e"})
	if msg := recv(t, gateConn); msg["type"] != "start-rejected" || msg["reason"] != "ワールドが存在しません" {
		t.Fatalf("expected start-rejected(ワールドが存在しません), got %+v", msg)
	}

	t.Log("/start clean: expect evacuate-request -> hardcore-ready (nothing was running, so evacuate is actually a no-op on Gate's side)")
	doStartOrLoad(t, gateConn, map[string]any{"type": "start", "clean": true, "requestedBy": "e2e"}, "hardcore-ready")

	send(t, gateConn, map[string]any{"type": "state-query"})
	if msg := recv(t, gateConn); msg["state"] != "ready" || msg["running"] != "true" {
		t.Fatalf("state-response after start clean = %+v, want {ready true}", msg)
	}

	props, err := os.ReadFile(l.propsPath)
	if err != nil {
		t.Fatalf("read server.properties: %v", err)
	}
	if !strings.Contains(string(props), "hardcore=true") {
		t.Fatalf("server.properties = %q, want hardcore=true enforced by Start", props)
	}

	t.Log("waiting for fakehardcore's automatic archive-request to land on disk")
	waitForNonEmptyDir(t, l.archiveDir, 5*time.Second)

	worldBefore, err := os.ReadFile(filepath.Join(l.worldDir, "level.dat"))
	if err != nil {
		t.Fatalf("read world marker before load: %v", err)
	}

	t.Log("/deactivate: expect evacuate-request -> evacuate-complete -> deactivate-complete, running preserved")
	doStartOrLoad(t, gateConn, map[string]any{"type": "deactivate", "requestedBy": "e2e"}, "deactivate-complete")

	send(t, gateConn, map[string]any{"type": "state-query"})
	if msg := recv(t, gateConn); msg["state"] != "stopped" || msg["running"] != "true" {
		t.Fatalf("state-response after deactivate = %+v, want {stopped true} (challenge paused, not lost)", msg)
	}

	t.Log("/start (clean:false) again: process was stopped but world/running survive, expect immediate hardcore-ready with no evacuate")
	send(t, gateConn, map[string]any{"type": "start", "clean": false, "requestedBy": "e2e"})
	if msg := recv(t, gateConn); msg["type"] != "hardcore-ready" {
		t.Fatalf("expected hardcore-ready with no evacuate-request in between (nothing was running to evacuate), got %+v", msg)
	}

	send(t, gateConn, map[string]any{"type": "state-query"})
	if msg := recv(t, gateConn); msg["state"] != "ready" || msg["running"] != "true" {
		t.Fatalf("state-response after resume = %+v, want {ready true}", msg)
	}

	t.Log("/load latest force=true: expect evacuate-request -> hardcore-ready, restoring the archive")
	doStartOrLoad(t, gateConn, map[string]any{"type": "load", "name": "latest", "force": true, "requestedBy": "e2e"}, "hardcore-ready")

	send(t, gateConn, map[string]any{"type": "state-query"})
	if msg := recv(t, gateConn); msg["state"] != "ready" || msg["running"] != "true" {
		t.Fatalf("state-response after load = %+v, want {ready true}", msg)
	}

	worldAfter, err := os.ReadFile(filepath.Join(l.worldDir, "level.dat"))
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
	if err := proc.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal manager: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- proc.cmd.Wait() }()
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
	proc.cmd.Process = nil // already reaped; skip the Cleanup's Kill/Wait

	if !strings.Contains(proc.output.String(), "fakehardcore: got SIGTERM") {
		t.Error("expected the hardcore child process to have received SIGTERM too (graceful shutdown)")
	}
}

// TestE2E_PersistsRunningAcrossRestart is the regression test for the
// original /start deadlock fix (architecture-manager.md 2節・変更履歴): a
// Manager restart must not lose the running value, and a truly fresh
// deploy (no state.json yet) must accept its first /start clean
// immediately instead of refusing it as "unknown".
func TestE2E_PersistsRunningAcrossRestart(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns real subprocesses and builds binaries; skipped in -short mode")
	}

	binDir := t.TempDir()
	l := newManagerLayout(t, binDir)

	proc1 := l.start(t)
	gateConn1 := l.dialGate(t)

	t.Log("first-ever /start clean must be accepted immediately (no prior state.json)")
	doStartOrLoad(t, gateConn1, map[string]any{"type": "start", "clean": true, "requestedBy": "e2e"}, "hardcore-ready")

	t.Log("graceful shutdown so the next manager doesn't see this one as an orphan")
	if err := proc1.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal manager: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- proc1.cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("first manager did not exit within 10s of SIGTERM")
	}
	proc1.cmd.Process = nil

	data, err := os.ReadFile(l.statePath)
	if err != nil {
		t.Fatalf("read state.json: %v", err)
	}
	if !strings.Contains(string(data), "true") {
		t.Fatalf("state.json = %q, want it to record running=true", data)
	}

	t.Log("restarting manager against the same workDir")
	proc2 := l.start(t)
	gateConn2 := l.dialGate(t)

	send(t, gateConn2, map[string]any{"type": "state-query"})
	if msg := recv(t, gateConn2); msg["state"] != "stopped" || msg["running"] != "true" {
		t.Fatalf("state-response after restart = %+v, want {stopped true} (persisted running, not unknown)", msg)
	}

	t.Log("/start (clean:false) after restart must resume the persisted challenge without any evacuate")
	send(t, gateConn2, map[string]any{"type": "start", "clean": false, "requestedBy": "e2e"})
	if msg := recv(t, gateConn2); msg["type"] != "hardcore-ready" {
		t.Fatalf("expected hardcore-ready, got %+v", msg)
	}

	if err := proc2.cmd.Process.Kill(); err != nil {
		t.Fatalf("kill second manager: %v", err)
	}
	_ = proc2.cmd.Wait()
	proc2.cmd.Process = nil
}

// TestE2E_ReapsOrphanAfterCrash is the regression test for orphan detection
// (architecture-manager.md 3節): if Manager is killed without a chance to
// shut down gracefully (SIGKILL, simulating panic/OOM), the hardcore child
// it leaves behind must be detected and terminated by the next Manager
// instance before that instance accepts any commands — otherwise a
// subsequent /start（clean無し）would double-start onto the same world/.
func TestE2E_ReapsOrphanAfterCrash(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns real subprocesses and builds binaries; skipped in -short mode")
	}

	binDir := t.TempDir()
	l := newManagerLayout(t, binDir)

	proc1 := l.start(t)
	gateConn1 := l.dialGate(t)
	doStartOrLoad(t, gateConn1, map[string]any{"type": "start", "clean": true, "requestedBy": "e2e"}, "hardcore-ready")

	pidData, err := os.ReadFile(l.pidFilePath)
	if err != nil {
		t.Fatalf("read pid file: %v", err)
	}
	orphanPID, err := strconv.Atoi(strings.TrimSpace(string(pidData)))
	if err != nil {
		t.Fatalf("parse pid file %q: %v", pidData, err)
	}

	t.Log("SIGKILL the manager itself, simulating a crash: no graceful shutdown, fakehardcore is left running")
	if err := proc1.cmd.Process.Kill(); err != nil {
		t.Fatalf("kill manager: %v", err)
	}
	_ = proc1.cmd.Wait()
	proc1.cmd.Process = nil

	if err := syscall.Kill(orphanPID, 0); err != nil {
		t.Fatalf("expected the orphaned fakehardcore (pid=%d) to still be alive right after the manager crash, signal check: %v", orphanPID, err)
	}

	t.Log("restarting manager: it must detect and terminate the orphaned fakehardcore before accepting commands")
	proc2 := l.start(t)
	_ = l.dialGate(t) // ReapOrphan must finish before the gate listener even accepts, so this dial is itself part of the assertion

	deadline := time.Now().Add(5 * time.Second)
	for {
		err := syscall.Kill(orphanPID, 0)
		if err != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("orphaned process (pid=%d) was never reaped by the restarted manager", orphanPID)
		}
		time.Sleep(50 * time.Millisecond)
	}

	if _, err := os.Stat(l.pidFilePath); !os.IsNotExist(err) {
		t.Errorf("expected the stale pid file to be removed after reaping, stat err = %v", err)
	}

	if err := proc2.cmd.Process.Kill(); err != nil {
		t.Fatalf("kill second manager: %v", err)
	}
	_ = proc2.cmd.Wait()
	proc2.cmd.Process = nil
}
