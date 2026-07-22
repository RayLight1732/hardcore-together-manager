// Package osprocess implements port.ProcessRunner and port.WorldPreparer by
// managing the hardcore server as an os/exec child process
// (architecture-manager.md 3節): start/stop with a SIGTERM→SIGKILL
// escalation, and (worldgen.go) preparing world/ for a fresh /start or a
// /load restore.
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

	mu     sync.Mutex
	cmd    *exec.Cmd
	exited chan struct{}
}

// New builds a Runner that launches command (argv[0] is the executable)
// with workDir as its working directory. world/ and server.properties are
// resolved relative to workDir (spec 11節).
func New(workDir string, command []string) *Runner {
	return &Runner{
		workDir:              workDir,
		worldDir:             filepath.Join(workDir, "world"),
		serverPropertiesPath: filepath.Join(workDir, "server.properties"),
		command:              command,
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
		close(exited)
	}()

	return nil
}

// Stop signals the running process to shut down gracefully (SIGTERM), waits
// up to killTimeout, and escalates to SIGKILL only if it hasn't exited by
// then (architecture-manager.md 3節). It returns immediately (nil) if no
// process is currently running.
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

func logLines(prefix string, r io.Reader) {
	scanner := bufio.NewScanner(r)
	// Server log lines can be long (stack traces); grow past bufio's 64KiB default.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		log.Printf("[%s] %s", prefix, scanner.Text())
	}
}
