package application

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/RayLight1732/hardcore-together-manager/internal/adapter/fsstate"
	"github.com/RayLight1732/hardcore-together-manager/internal/domain/challenge"
	"github.com/RayLight1732/hardcore-together-manager/internal/domain/records"
)

type rejectedCall struct{ kind, reason string }

type fakeGate struct {
	mu sync.Mutex

	evacuateCalls []string
	evacuateErr   error
	evacuateDelay time.Duration

	hardcoreReadyCalls int
	hardcoreReadyErr   error

	deactivateCompleteCalls int
	deactivateCompleteErr   error

	rejectedCalls []rejectedCall
}

func (f *fakeGate) RequestEvacuate(ctx context.Context, reason string) error {
	f.mu.Lock()
	f.evacuateCalls = append(f.evacuateCalls, reason)
	f.mu.Unlock()
	if f.evacuateDelay > 0 {
		select {
		case <-time.After(f.evacuateDelay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return f.evacuateErr
}

func (f *fakeGate) SendHardcoreReady() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hardcoreReadyCalls++
	return f.hardcoreReadyErr
}

func (f *fakeGate) SendDeactivateComplete() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deactivateCompleteCalls++
	return f.deactivateCompleteErr
}

func (f *fakeGate) SendRejected(kind, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rejectedCalls = append(f.rejectedCalls, rejectedCall{kind, reason})
	return nil
}

type fakeReady struct {
	mu      sync.Mutex
	running bool
	err     error
	drained int
}

func (f *fakeReady) WaitForReady(ctx context.Context) (bool, error) {
	return f.running, f.err
}

func (f *fakeReady) DrainReady() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.drained++
}

type fakeProcess struct {
	mu         sync.Mutex
	startErr   error
	stopErr    error
	startCalls int
	stopCalls  int
	running    bool
}

func (f *fakeProcess) Start() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.startCalls++
	if f.startErr == nil {
		f.running = true
	}
	return f.startErr
}

func (f *fakeProcess) Stop(ctx context.Context, killTimeout time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopCalls++
	if f.stopErr == nil {
		f.running = false
	}
	return f.stopErr
}

func (f *fakeProcess) IsRunning() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.running
}

type fakeWorld struct {
	mu          sync.Mutex
	wipeCalls   int
	ensureCalls int
	wipeErr     error
	ensureErr   error
	exists      bool
	existsErr   error
	order       *[]string
}

func (f *fakeWorld) WipeWorld() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.wipeCalls++
	if f.order != nil {
		*f.order = append(*f.order, "wipe")
	}
	return f.wipeErr
}

func (f *fakeWorld) EnsureHardcoreMode() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensureCalls++
	return f.ensureErr
}

func (f *fakeWorld) Exists() (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.exists, f.existsErr
}

type fakeArchive struct {
	mu           sync.Mutex
	exists       map[string]bool
	latestName   string
	latestErr    error
	restoreErr   error
	restoreCalls []string
	saveErr      error
	saveName     string
	order        *[]string
}

func (f *fakeArchive) Exists(name string) (bool, error) { return f.exists[name], nil }
func (f *fakeArchive) Latest() (string, error)          { return f.latestName, f.latestErr }

func (f *fakeArchive) Restore(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.restoreCalls = append(f.restoreCalls, name)
	if f.order != nil {
		*f.order = append(*f.order, "restore")
	}
	return f.restoreErr
}

func (f *fakeArchive) Save(name string, elapsedTime int64, now time.Time) (string, error) {
	if f.saveErr != nil {
		return "", f.saveErr
	}
	if f.saveName != "" {
		return f.saveName, nil
	}
	return name, nil
}

type fakeRecords struct {
	all []records.ChallengeRecord
	err error
}

func (f *fakeRecords) ReadAll() ([]records.ChallengeRecord, error) { return f.all, f.err }

type fakeClock struct{ now time.Time }

func (f fakeClock) Now() time.Time { return f.now }

type harness struct {
	svc     *ChallengeApplicationService
	state   *fsstate.Repository
	gate    *fakeGate
	ready   *fakeReady
	process *fakeProcess
	world   *fakeWorld
	archive *fakeArchive
	records *fakeRecords
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	state, err := fsstate.New(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("fsstate.New: %v", err)
	}

	h := &harness{
		state:   state,
		gate:    &fakeGate{},
		ready:   &fakeReady{running: true},
		process: &fakeProcess{},
		world:   &fakeWorld{},
		archive: &fakeArchive{exists: map[string]bool{}},
		records: &fakeRecords{},
	}

	h.svc = New(Deps{
		State:   h.state,
		Process: h.process,
		World:   h.world,
		Archive: h.archive,
		Records: h.records,
		Gate:    h.gate,
		Ready:   h.ready,
		Clock:   fakeClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
	}, Timeouts{
		Evacuate:    200 * time.Millisecond,
		Ready:       200 * time.Millisecond,
		ProcessStop: 200 * time.Millisecond,
	})

	return h
}

// --- /start clean ---

func TestStartClean_AllowedEvenWhileRunning(t *testing.T) {
	h := newHarness(t)
	h.state.MarkReady(true)
	h.process.running = true

	if err := h.svc.Start(context.Background(), true, "OP"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if len(h.gate.rejectedCalls) != 0 {
		t.Fatalf("clean=true must not be rejected while running, got %+v", h.gate.rejectedCalls)
	}
	if len(h.gate.evacuateCalls) != 1 || h.gate.evacuateCalls[0] != "force-reset" {
		t.Errorf("evacuateCalls = %v, want [force-reset]", h.gate.evacuateCalls)
	}
	if h.process.stopCalls != 1 || h.world.wipeCalls != 1 || h.process.startCalls != 1 {
		t.Errorf("stopCalls=%d wipeCalls=%d startCalls=%d, want 1 each", h.process.stopCalls, h.world.wipeCalls, h.process.startCalls)
	}
	if h.gate.hardcoreReadyCalls != 1 {
		t.Errorf("hardcoreReadyCalls = %d, want 1", h.gate.hardcoreReadyCalls)
	}
}

func TestStartClean_SkipsEvacuateWhenAlreadyStopped(t *testing.T) {
	h := newHarness(t)
	// Fresh state: {stopped, false} (world/未生成の初回相当).

	if err := h.svc.Start(context.Background(), true, "OP"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if len(h.gate.evacuateCalls) != 0 {
		t.Errorf("evacuateCalls = %v, want none: nothing was running, evacuate is unnecessary", h.gate.evacuateCalls)
	}
	if h.process.stopCalls != 0 {
		t.Errorf("stopCalls = %d, want 0", h.process.stopCalls)
	}
	if h.world.wipeCalls != 1 || h.process.startCalls != 1 {
		t.Errorf("wipeCalls=%d startCalls=%d, want 1 each", h.world.wipeCalls, h.process.startCalls)
	}
	if h.gate.hardcoreReadyCalls != 1 {
		t.Errorf("hardcoreReadyCalls = %d, want 1", h.gate.hardcoreReadyCalls)
	}
}

func TestStartClean_RejectsMidTransition(t *testing.T) {
	h := newHarness(t)
	h.state.MarkReady(false)
	if ok, _ := h.state.TryMarkStarting(false); !ok {
		t.Fatal("setup: expected TryMarkStarting to succeed")
	}
	// state is now {starting, unknown}: a real in-flight sequence.

	if err := h.svc.Start(context.Background(), true, "OP"); err != nil {
		t.Fatalf("Start returned error (rejection isn't an error return): %v", err)
	}
	if len(h.gate.rejectedCalls) != 1 || h.gate.rejectedCalls[0].kind != "start-rejected" {
		t.Fatalf("rejectedCalls = %+v, want one start-rejected", h.gate.rejectedCalls)
	}
}

func TestStartClean_EvacuateFailureRevertsState(t *testing.T) {
	h := newHarness(t)
	h.state.MarkReady(true)
	h.process.running = true
	prior := h.state.Snapshot()
	h.gate.evacuateErr = errors.New("gate unreachable")

	if err := h.svc.Start(context.Background(), true, "OP"); err == nil {
		t.Fatal("expected an error when evacuate fails")
	}
	if h.process.stopCalls != 0 {
		t.Error("process must not be touched if evacuate never completed")
	}
	if snap := h.state.Snapshot(); snap != prior {
		t.Fatalf("snapshot after evacuate failure = %+v, want reverted to %+v", snap, prior)
	}
}

func TestStartClean_StopFailureMarksUnknownButStaysStarting(t *testing.T) {
	h := newHarness(t)
	h.state.MarkReady(true)
	h.process.running = true
	h.process.stopErr = errors.New("signal failed")

	if err := h.svc.Start(context.Background(), true, "OP"); err == nil {
		t.Fatal("expected an error when stop fails")
	}
	snap := h.state.Snapshot()
	if snap.Phase != challenge.PhaseStarting {
		t.Errorf("phase = %v, want starting (stuck until forced)", snap.Phase)
	}
	if snap.Running != challenge.RunningUnknown {
		t.Errorf("running = %v, want unknown", snap.Running)
	}
}

func TestStartClean_PrepareWorldFailureMarksStopped(t *testing.T) {
	h := newHarness(t)
	h.state.MarkReady(false)
	h.world.wipeErr = errors.New("disk full")

	if err := h.svc.Start(context.Background(), true, "OP"); err == nil {
		t.Fatal("expected an error when world prep fails")
	}
	if h.process.startCalls != 0 {
		t.Error("process must not be started if world prep failed")
	}
	if snap := h.state.Snapshot(); snap.Phase != challenge.PhaseStopped || snap.Running != challenge.RunningFalse {
		t.Fatalf("snapshot = %+v, want {stopped false}", snap)
	}
}

func TestStartClean_ProcessStartFailureMarksStoppedAndNotRunning(t *testing.T) {
	h := newHarness(t)
	h.state.MarkReady(true) // running was true — clean's wipe destroys it regardless
	h.process.startErr = errors.New("exec: not found")

	if err := h.svc.Start(context.Background(), true, "OP"); err == nil {
		t.Fatal("expected an error when process.Start fails")
	}
	if snap := h.state.Snapshot(); snap.Phase != challenge.PhaseStopped || snap.Running != challenge.RunningFalse {
		t.Fatalf("snapshot = %+v, want {stopped false} (world was already wiped, so nothing is in progress)", snap)
	}
}

func TestStartClean_ReadyTimeoutMarksUnknownButStaysStarting(t *testing.T) {
	h := newHarness(t)
	h.state.MarkReady(false)
	h.ready.err = context.DeadlineExceeded

	if err := h.svc.Start(context.Background(), true, "OP"); err == nil {
		t.Fatal("expected an error when ready-wait times out")
	}
	snap := h.state.Snapshot()
	if snap.Phase != challenge.PhaseStarting || snap.Running != challenge.RunningUnknown {
		t.Fatalf("snapshot = %+v, want {starting unknown}", snap)
	}
	if h.gate.hardcoreReadyCalls != 0 {
		t.Error("hardcore-ready must not be sent if ready-wait failed")
	}
}

// --- /start（clean無し） ---

func TestStartResume_RejectsWhenWorldMissing(t *testing.T) {
	h := newHarness(t)
	h.world.exists = false

	if err := h.svc.Start(context.Background(), false, "OP"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if len(h.gate.rejectedCalls) != 1 || h.gate.rejectedCalls[0].kind != "start-rejected" {
		t.Fatalf("rejectedCalls = %+v", h.gate.rejectedCalls)
	}
	if want := "ワールドが存在しません"; h.gate.rejectedCalls[0].reason != want {
		t.Errorf("reason = %q, want %q", h.gate.rejectedCalls[0].reason, want)
	}
	if h.process.startCalls != 0 {
		t.Error("process must not be started when world is missing")
	}
}

func TestStartResume_RejectsWhileAlreadyRunning(t *testing.T) {
	h := newHarness(t)
	h.world.exists = true
	h.state.MarkReady(true) // phase=ready: a process is already running

	if err := h.svc.Start(context.Background(), false, "OP"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if len(h.gate.rejectedCalls) != 1 {
		t.Fatalf("rejectedCalls = %+v", h.gate.rejectedCalls)
	}
	if want := "既に起動しています"; h.gate.rejectedCalls[0].reason != want {
		t.Errorf("reason = %q, want %q", h.gate.rejectedCalls[0].reason, want)
	}
}

func TestStartResume_HappyPath_NeverTouchesWorldOrEvacuates(t *testing.T) {
	h := newHarness(t)
	h.world.exists = true
	h.state.SetRunning(true) // an in-progress challenge, process just happens to be stopped (state④)

	if err := h.svc.Start(context.Background(), false, "OP"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if h.world.wipeCalls != 0 || h.world.ensureCalls != 0 {
		t.Errorf("wipeCalls=%d ensureCalls=%d, want 0 (resume must never touch world/)", h.world.wipeCalls, h.world.ensureCalls)
	}
	if len(h.gate.evacuateCalls) != 0 {
		t.Errorf("evacuateCalls = %v, want none (nothing was running to evacuate from)", h.gate.evacuateCalls)
	}
	if h.process.startCalls != 1 {
		t.Errorf("startCalls = %d, want 1", h.process.startCalls)
	}
	if h.gate.hardcoreReadyCalls != 1 {
		t.Errorf("hardcoreReadyCalls = %d, want 1", h.gate.hardcoreReadyCalls)
	}
	// running is whatever the mod reports on ready (h.ready.running=true by
	// default), reflecting that the challenge picked back up unchanged.
	if snap := h.state.Snapshot(); snap.Running != challenge.RunningTrue {
		t.Errorf("running = %v, want true (preserved through resume)", snap.Running)
	}
}

func TestStartResume_ProcessStartFailurePreservesRunning(t *testing.T) {
	h := newHarness(t)
	h.world.exists = true
	h.state.SetRunning(true)
	h.process.startErr = errors.New("exec: not found")

	if err := h.svc.Start(context.Background(), false, "OP"); err == nil {
		t.Fatal("expected an error when process.Start fails")
	}
	if snap := h.state.Snapshot(); snap.Phase != challenge.PhaseStopped || snap.Running != challenge.RunningTrue {
		t.Fatalf("snapshot = %+v, want {stopped true}: world was never touched, so the in-progress challenge must survive", snap)
	}
}

func TestStartResume_MidTransitionRejected(t *testing.T) {
	h := newHarness(t)
	h.world.exists = true
	if ok, _ := h.state.TryMarkResuming(); !ok {
		t.Fatal("setup: expected TryMarkResuming to succeed")
	}

	if err := h.svc.Start(context.Background(), false, "OP"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if len(h.gate.rejectedCalls) != 1 || h.gate.rejectedCalls[0].reason != "処理中です。しばらくお待ちください" {
		t.Fatalf("rejectedCalls = %+v", h.gate.rejectedCalls)
	}
}

// --- /load ---

func TestLoad_RejectsWhenArchiveMissing_RevertsState(t *testing.T) {
	h := newHarness(t)
	h.state.MarkReady(false)
	prior := h.state.Snapshot()

	if err := h.svc.Load(context.Background(), "save1", false, "OP"); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(h.gate.rejectedCalls) != 1 || h.gate.rejectedCalls[0].kind != "load-rejected" {
		t.Fatalf("rejectedCalls = %+v", h.gate.rejectedCalls)
	}
	if want := "アーカイブsave1は存在しません"; h.gate.rejectedCalls[0].reason != want {
		t.Errorf("reason = %q, want %q", h.gate.rejectedCalls[0].reason, want)
	}
	if snap := h.state.Snapshot(); snap != prior {
		t.Fatalf("snapshot = %+v, want reverted to %+v", snap, prior)
	}
	if len(h.archive.restoreCalls) != 0 {
		t.Error("Restore must not be called when the archive doesn't exist")
	}
}

func TestLoad_LatestResolvesAndRestores(t *testing.T) {
	h := newHarness(t)
	h.state.MarkReady(false)
	h.archive.latestName = "newest"

	if err := h.svc.Load(context.Background(), "latest", false, "OP"); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(h.archive.restoreCalls) != 1 || h.archive.restoreCalls[0] != "newest" {
		t.Fatalf("restoreCalls = %v, want [newest]", h.archive.restoreCalls)
	}
}

func TestLoad_NoArchivesRejectsForLatest(t *testing.T) {
	h := newHarness(t)
	h.state.MarkReady(false)
	h.archive.latestErr = errors.New("no archives")

	if err := h.svc.Load(context.Background(), "latest", false, "OP"); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(h.gate.rejectedCalls) != 1 || h.gate.rejectedCalls[0].kind != "load-rejected" {
		t.Fatalf("rejectedCalls = %+v", h.gate.rejectedCalls)
	}
}

func TestLoad_RunningCheckTakesPriorityOverArchiveCheck(t *testing.T) {
	h := newHarness(t)
	h.state.MarkReady(true) // running=true AND the archive is also missing

	if err := h.svc.Load(context.Background(), "missing", false, "OP"); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(h.gate.rejectedCalls) != 1 {
		t.Fatalf("rejectedCalls = %+v", h.gate.rejectedCalls)
	}
	if want := "挑戦が進行中です"; h.gate.rejectedCalls[0].reason != want {
		t.Errorf("reason = %q, want %q (running check must win)", h.gate.rejectedCalls[0].reason, want)
	}
}

func TestLoad_ExistingArchiveHappyPath(t *testing.T) {
	h := newHarness(t)
	h.state.MarkReady(false)
	h.archive.exists["save1"] = true

	if err := h.svc.Load(context.Background(), "save1", false, "OP"); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(h.archive.restoreCalls) != 1 || h.archive.restoreCalls[0] != "save1" {
		t.Fatalf("restoreCalls = %v, want [save1]", h.archive.restoreCalls)
	}
	if h.world.wipeCalls != 1 {
		t.Errorf("wipeCalls = %d, want 1 (world/ must be cleared before Restore copies into it)", h.world.wipeCalls)
	}
	if h.world.ensureCalls != 1 {
		t.Errorf("ensureCalls = %d, want 1 (server.properties guard applies to Load too)", h.world.ensureCalls)
	}
}

func TestLoad_WipesWorldBeforeRestoring(t *testing.T) {
	h := newHarness(t)
	h.state.MarkReady(false)
	h.archive.exists["save1"] = true

	var order []string
	h.world.order = &order
	h.archive.order = &order

	if err := h.svc.Load(context.Background(), "save1", false, "OP"); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(order) != 2 || order[0] != "wipe" || order[1] != "restore" {
		t.Fatalf("operation order = %v, want [wipe restore]", order)
	}
}

func TestLoad_SkipsEvacuateWhenAlreadyStopped(t *testing.T) {
	h := newHarness(t)
	// Fresh state: {stopped, false}.
	h.archive.exists["save1"] = true

	if err := h.svc.Load(context.Background(), "save1", false, "OP"); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(h.gate.evacuateCalls) != 0 {
		t.Errorf("evacuateCalls = %v, want none", h.gate.evacuateCalls)
	}
	if h.process.stopCalls != 0 {
		t.Errorf("stopCalls = %d, want 0", h.process.stopCalls)
	}
	if h.gate.hardcoreReadyCalls != 1 {
		t.Errorf("hardcoreReadyCalls = %d, want 1", h.gate.hardcoreReadyCalls)
	}
}

// --- /deactivate ---

func TestDeactivate_RejectsWhileStopped(t *testing.T) {
	h := newHarness(t)
	// Fresh state: {stopped, false}.

	if err := h.svc.Deactivate(context.Background(), "OP"); err != nil {
		t.Fatalf("Deactivate: %v", err)
	}
	if len(h.gate.rejectedCalls) != 1 || h.gate.rejectedCalls[0].kind != "deactivate-rejected" {
		t.Fatalf("rejectedCalls = %+v", h.gate.rejectedCalls)
	}
	if want := "既に停止しています"; h.gate.rejectedCalls[0].reason != want {
		t.Errorf("reason = %q, want %q", h.gate.rejectedCalls[0].reason, want)
	}
}

func TestDeactivate_RejectsMidTransition(t *testing.T) {
	h := newHarness(t)
	h.state.MarkReady(true)
	if ok, _ := h.state.TryMarkDeactivating(); !ok {
		t.Fatal("setup: expected TryMarkDeactivating to succeed")
	}

	if err := h.svc.Deactivate(context.Background(), "OP"); err != nil {
		t.Fatalf("Deactivate: %v", err)
	}
	if len(h.gate.rejectedCalls) != 1 || h.gate.rejectedCalls[0].reason != "処理中です。しばらくお待ちください" {
		t.Fatalf("rejectedCalls = %+v", h.gate.rejectedCalls)
	}
}

func TestDeactivate_HappyPath_PreservesRunning(t *testing.T) {
	h := newHarness(t)
	h.state.MarkReady(true)
	h.process.running = true

	if err := h.svc.Deactivate(context.Background(), "OP"); err != nil {
		t.Fatalf("Deactivate: %v", err)
	}
	if len(h.gate.evacuateCalls) != 1 || h.gate.evacuateCalls[0] != "deactivate" {
		t.Errorf("evacuateCalls = %v, want [deactivate]", h.gate.evacuateCalls)
	}
	if h.process.stopCalls != 1 {
		t.Errorf("stopCalls = %d, want 1", h.process.stopCalls)
	}
	if h.gate.deactivateCompleteCalls != 1 {
		t.Errorf("deactivateCompleteCalls = %d, want 1", h.gate.deactivateCompleteCalls)
	}
	if snap := h.state.Snapshot(); snap.Phase != challenge.PhaseStopped || snap.Running != challenge.RunningTrue {
		t.Fatalf("snapshot = %+v, want {stopped true} (running preserved)", snap)
	}
}

func TestDeactivate_EvacuateFailureRevertsState(t *testing.T) {
	h := newHarness(t)
	h.state.MarkReady(true)
	prior := h.state.Snapshot()
	h.gate.evacuateErr = errors.New("gate unreachable")

	if err := h.svc.Deactivate(context.Background(), "OP"); err == nil {
		t.Fatal("expected an error when evacuate fails")
	}
	if h.process.stopCalls != 0 {
		t.Error("process must not be touched if evacuate never completed")
	}
	if snap := h.state.Snapshot(); snap != prior {
		t.Fatalf("snapshot after evacuate failure = %+v, want reverted to %+v", snap, prior)
	}
}

func TestDeactivate_StopFailureMarksUnknown(t *testing.T) {
	h := newHarness(t)
	h.state.MarkReady(true)
	h.process.stopErr = errors.New("signal failed")

	if err := h.svc.Deactivate(context.Background(), "OP"); err == nil {
		t.Fatal("expected an error when stop fails")
	}
	snap := h.state.Snapshot()
	if snap.Phase != challenge.PhaseStopping {
		t.Errorf("phase = %v, want stopping (stuck until an operator investigates)", snap.Phase)
	}
	if snap.Running != challenge.RunningUnknown {
		t.Errorf("running = %v, want unknown", snap.Running)
	}
	if h.gate.deactivateCompleteCalls != 0 {
		t.Error("deactivate-complete must not be sent if stop failed")
	}
}

// --- opMutex ---

func TestOpMutex_SerializesArchiveRequestAndStartClean(t *testing.T) {
	h := newHarness(t)
	h.state.MarkReady(false)

	blockArchive := make(chan struct{})
	unblockArchive := make(chan struct{})
	h.archive.saveName = "blocked"
	// Wrap Save to block until the test says so, simulating a slow
	// archive-request in flight while /start comes in.
	orig := h.archive
	blocking := &blockingArchive{fakeArchive: orig, entered: blockArchive, release: unblockArchive}
	h.svc.deps.Archive = blocking

	go func() {
		_, _ = h.svc.HandleArchiveRequest("manualsave", 1)
	}()

	select {
	case <-blockArchive:
	case <-time.After(2 * time.Second):
		t.Fatal("HandleArchiveRequest never entered Save")
	}

	startDone := make(chan error, 1)
	go func() {
		startDone <- h.svc.Start(context.Background(), true, "OP")
	}()

	select {
	case <-startDone:
		t.Fatal("Start returned before the in-flight archive-request released opMutex")
	case <-time.After(100 * time.Millisecond):
	}

	close(unblockArchive)
	select {
	case err := <-startDone:
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Start never completed after the archive-request released the lock")
	}
}

type blockingArchive struct {
	*fakeArchive
	entered chan struct{}
	release chan struct{}
}

func (b *blockingArchive) Save(name string, elapsedTime int64, now time.Time) (string, error) {
	close(b.entered)
	<-b.release
	return b.fakeArchive.Save(name, elapsedTime, now)
}

// --- misc handlers ---

func TestHandleReady_UpdatesState(t *testing.T) {
	h := newHarness(t)
	h.svc.HandleReady(true)
	if snap := h.state.Snapshot(); snap.Phase != challenge.PhaseReady || snap.Running != challenge.RunningTrue {
		t.Fatalf("snapshot = %+v, want {ready true}", snap)
	}
}

func TestHandleRunningChanged_UpdatesRunningOnly(t *testing.T) {
	h := newHarness(t)
	h.state.MarkReady(true)
	h.svc.HandleRunningChanged(false)
	if snap := h.state.Snapshot(); snap.Phase != challenge.PhaseReady || snap.Running != challenge.RunningFalse {
		t.Fatalf("snapshot = %+v, want {ready false}", snap)
	}
}

func TestHandleDisconnect_MarksUnknownWhileProcessAlive(t *testing.T) {
	h := newHarness(t)
	h.state.MarkReady(true)
	h.process.running = true
	h.svc.HandleDisconnect()
	if snap := h.state.Snapshot(); snap.Running != challenge.RunningUnknown {
		t.Fatalf("running = %v, want unknown", snap.Running)
	}
}

func TestHandleDisconnect_PreservesRunningWhenProcessDead(t *testing.T) {
	h := newHarness(t)
	h.state.MarkReady(true)
	h.process.running = false // the process itself is gone, only the TCP link dropped is not the case here

	h.svc.HandleDisconnect()
	if snap := h.state.Snapshot(); snap.Running != challenge.RunningTrue {
		t.Fatalf("running = %v, want true (unchanged): a dead process can't send a late correction, so unknown would be a permanent false alarm", snap.Running)
	}
}

// TestHandleDisconnect_IgnoredMidTransition guards the race fixed above:
// a disconnect that fires while Deactivate (or Start clean/Load) is
// already tearing the process down must never clobber the state that
// operation's own MarkStopped/MarkDeactivated call is about to (or just
// did) commit — regardless of what Process.IsRunning() happens to read at
// that instant.
func TestHandleDisconnect_IgnoredMidTransition(t *testing.T) {
	h := newHarness(t)
	h.state.MarkReady(true)
	if ok, _ := h.state.TryMarkDeactivating(); !ok {
		t.Fatal("setup: expected TryMarkDeactivating to succeed")
	}
	h.process.running = true // still true at the instant the disconnect fires, per the race this guards against

	h.svc.HandleDisconnect()
	if snap := h.state.Snapshot(); snap.Running != challenge.RunningTrue {
		t.Fatalf("running = %v, want unchanged true: mid-transition disconnects must be ignored entirely", snap.Running)
	}
}

func TestHandleArchiveRequest_DelegatesToArchiveRepository(t *testing.T) {
	h := newHarness(t)
	h.archive.saveName = "final-name"

	got, err := h.svc.HandleArchiveRequest("requested", 42)
	if err != nil {
		t.Fatalf("HandleArchiveRequest: %v", err)
	}
	if got != "final-name" {
		t.Errorf("got %q, want final-name", got)
	}
}

func TestSnapshot(t *testing.T) {
	h := newHarness(t)
	h.state.MarkReady(true)
	if got := h.svc.Snapshot(); got.Phase != challenge.PhaseReady || got.Running != challenge.RunningTrue {
		t.Fatalf("Snapshot() = %+v", got)
	}
}

func TestSaveData_AggregatesRecords(t *testing.T) {
	h := newHarness(t)
	h.records.all = []records.ChallengeRecord{
		{ChallengeID: "A", Events: []records.Event{{Type: records.EventClear}}},
	}
	got, err := h.svc.SaveData()
	if err != nil {
		t.Fatalf("SaveData: %v", err)
	}
	if len(got) != 1 || got[0].ChallengeID != "A" {
		t.Fatalf("got %+v", got)
	}
}

func TestSenpan_AggregatesRecords(t *testing.T) {
	h := newHarness(t)
	h.records.all = []records.ChallengeRecord{
		{ChallengeID: "A", Events: []records.Event{
			{Type: records.EventDeath, DeadPlayer: &records.PlayerRef{UUID: "u1", Name: "Steve"}},
		}},
	}
	got, err := h.svc.Senpan()
	if err != nil {
		t.Fatalf("Senpan: %v", err)
	}
	if len(got) != 1 || got[0].Count != 1 {
		t.Fatalf("got %+v", got)
	}
}
