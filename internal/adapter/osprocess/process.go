// Package osprocess implements port.ProcessRunner and port.WorldPreparer by
// managing the hardcore server as an os/exec child process
// (architecture-manager.md 3節): start/stop with a SIGTERM→SIGKILL
// escalation, and (worldgen.go) preparing world/ for a fresh /start or a
// /load restore. It also tracks the child's PID in a small file so a
// freshly (re)started Manager can detect and reap an orphaned hardcore
// process left behind by a crashed (not gracefully shut down) previous
// Manager instance — see ReapOrphan.
package osprocess

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/RayLight1732/hardcore-together-manager/internal/port"
)

var (
	_ port.ProcessRunner = (*Runner)(nil)
	_ port.WorldPreparer = (*Runner)(nil)
)

// Runner manages exactly one hardcore process at a time, rooted at workDir.
type Runner struct {
	workDir              string
	worldDir             string
	serverPropertiesPath string
	command              []string
	pidFilePath          string

	mu     sync.Mutex
	cmd    *exec.Cmd
	exited chan struct{}
}

// New builds a Runner that launches command (argv[0] is the executable)
// with workDir as its working directory. world/ and server.properties are
// resolved relative to workDir (spec 11節). pidFilePath (empty to disable)
// is where the running child's PID is recorded for orphan detection
// (ReapOrphan, architecture-manager.md 3節).
func New(workDir string, command []string, pidFilePath string) *Runner {
	return &Runner{
		workDir:              workDir,
		worldDir:             filepath.Join(workDir, "world"),
		serverPropertiesPath: filepath.Join(workDir, "server.properties"),
		command:              command,
		pidFilePath:          pidFilePath,
	}
}

// Start launches the hardcore process. It is an error to call Start while a
// process launched by this Runner is still running.
func (r *Runner) Start() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.cmd != nil {
		return errors.New("osprocess: already running")
	}
	if len(r.command) == 0 {
		return errors.New("osprocess: empty command")
	}

	cmd := exec.Command(r.command[0], r.command[1:]...)
	cmd.Dir = r.workDir

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("osprocess: stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("osprocess: stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("osprocess: start: %w", err)
	}

	r.writePIDFile(cmd.Process.Pid)

	exited := make(chan struct{})
	r.cmd = cmd
	r.exited = exited

	go logLines("hardcore", stdout)
	go logLines("hardcore[stderr]", stderr)
	go func() {
		waitErr := cmd.Wait()
		log.Printf("osprocess: hardcore process exited: %v", waitErr)
		r.mu.Lock()
		r.cmd = nil
		r.exited = nil
		r.mu.Unlock()
		r.removePIDFile()
		close(exited)
	}()

	return nil
}

// IsRunning reports whether a process launched by this Runner is currently
// alive (port.ProcessRunner, used by HandleDisconnect to distinguish "the
// process died" from "only the TCP connection dropped",
// docs/protocol-mod-manager.md 5節).
func (r *Runner) IsRunning() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cmd != nil
}

// Stop signals the running process to shut down gracefully (SIGTERM), waits
// up to killTimeout, and escalates to SIGKILL only if it hasn't exited by
// then (architecture-manager.md 3節). It returns immediately (nil) if no
// process is currently running. The PID file (if configured) is removed by
// Start's exit-watching goroutine once the process is confirmed gone, not
// here — so it's also cleaned up correctly if the process exits on its own
// rather than via Stop.
func (r *Runner) Stop(ctx context.Context, killTimeout time.Duration) error {
	r.mu.Lock()
	cmd := r.cmd
	exited := r.exited
	r.mu.Unlock()

	if cmd == nil {
		return nil
	}

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("osprocess: sigterm: %w", err)
	}

	timer := time.NewTimer(killTimeout)
	defer timer.Stop()

	select {
	case <-exited:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
	}

	if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("osprocess: sigkill: %w", err)
	}

	select {
	case <-exited:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *Runner) writePIDFile(pid int) {
	if r.pidFilePath == "" {
		return
	}
	if err := os.WriteFile(r.pidFilePath, []byte(strconv.Itoa(pid)), 0o644); err != nil {
		// Best-effort: the PID file only helps detect an orphan after a
		// Manager crash, it doesn't gate normal operation.
		log.Printf("osprocess: write pid file %s: %v", r.pidFilePath, err)
	}
}

func (r *Runner) removePIDFile() {
	if r.pidFilePath == "" {
		return
	}
	if err := os.Remove(r.pidFilePath); err != nil && !os.IsNotExist(err) {
		log.Printf("osprocess: remove pid file %s: %v", r.pidFilePath, err)
	}
}

// ReapOrphan checks pidFilePath (configured via New) for a PID left behind
// by a previous Manager instance that never got to run Stop — i.e. it
// crashed (panic/OOM/SIGKILL) instead of shutting down gracefully via
// SIGTERM (architecture-manager.md 3節・14節). Manager's caller (cmd/manager)
// must run this before accepting any Gate connections, so a stale orphan is
// never mistaken for "not running" and started over (double-start onto the
// same world/ and port).
//
// If no PID file exists, or the recorded PID is no longer alive, this is a
// no-op beyond removing a stale file. If it is alive, it is terminated
// (SIGTERM, escalating to SIGKILL after killTimeout, mirroring Stop's
// escalation) before returning. PID-reuse false positives (the recorded PID
// now belongs to an unrelated process, e.g. after a host reboot) are not
// guarded against — matching command-line verification is left as an open
// item (specification.md 14節未確定事項9).
func (r *Runner) ReapOrphan(killTimeout time.Duration) error {
	if r.pidFilePath == "" {
		return nil
	}

	data, err := os.ReadFile(r.pidFilePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("osprocess: read pid file %s: %w", r.pidFilePath, err)
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		log.Printf("osprocess: pid file %s has invalid content %q, discarding", r.pidFilePath, data)
		return r.removeStalePIDFile()
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		return r.removeStalePIDFile()
	}

	if !processAlive(proc) {
		return r.removeStalePIDFile()
	}

	log.Printf("osprocess: found an orphaned hardcore process (pid=%d) left behind by a previous Manager instance; terminating it", pid)

	if err := proc.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("osprocess: sigterm orphan pid %d: %w", pid, err)
	}
	if waitForDeath(proc, killTimeout) {
		return r.removeStalePIDFile()
	}

	if err := proc.Signal(syscall.SIGKILL); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("osprocess: sigkill orphan pid %d: %w", pid, err)
	}
	waitForDeath(proc, 5*time.Second)
	return r.removeStalePIDFile()
}

func (r *Runner) removeStalePIDFile() error {
	if err := os.Remove(r.pidFilePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("osprocess: remove stale pid file %s: %w", r.pidFilePath, err)
	}
	return nil
}

// processAlive checks liveness of a process we did not fork ourselves (so
// Wait cannot be used) via a signal-0 probe, the standard Unix idiom.
func processAlive(proc *os.Process) bool {
	return proc.Signal(syscall.Signal(0)) == nil
}

// waitForDeath polls until proc is no longer alive or timeout passes,
// returning whether it died in time. Used only for a foreign (non-child)
// process, where Wait isn't available.
func waitForDeath(proc *os.Process, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !processAlive(proc) {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return !processAlive(proc)
}

func logLines(prefix string, r io.Reader) {
	scanner := bufio.NewScanner(r)
	// Server log lines can be long (stack traces); grow past bufio's 64KiB default.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		log.Printf("[%s] %s", prefix, scanner.Text())
	}
}
