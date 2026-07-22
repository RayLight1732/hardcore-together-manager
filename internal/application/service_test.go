package application

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/RayLight1732/hardcore-together-manager/internal/adapter/memstate"
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
}

func (f *fakeProcess) Start() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.startCalls++
	return f.startErr
}

func (f *fakeProcess) Stop(ctx context.Context, killTimeout time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopCalls++
	return f.stopErr
}

type fakeWorld struct {
	mu          sync.Mutex
	wipeCalls   int
	ensureCalls int
	wipeErr     error
	ensureErr   error
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
	state   *memstate.Repository
	gate    *fakeGate
	ready   *fakeReady
	process *fakeProcess
	world   *fakeWorld
	archive *fakeArchive
	records *fakeRecords
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	h := &harness{
		state:   memstate.New(),
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

func TestStart_RejectsWhileRunning(t *testing.T) {
	h := newHarness(t)
	h.state.MarkReady(true)

	if err := h.svc.Start(context.Background(), false, "Steve"); err != nil {
		t.Fatalf("Start returned error (rejection isn't an error return): %v", err)
	}
	if len(h.gate.rejectedCalls) != 1 || h.gate.rejectedCalls[0].kind != "start-rejected" {
		t.Fatalf("rejectedCalls = %+v, want one start-rejected", h.gate.rejectedCalls)
	}
	if h.process.startCalls != 0 || h.world.wipeCalls != 0 {
		t.Error("no process/world operations should happen on a rejected start")
	}
	if snap := h.state.Snapshot(); snap.Phase != challenge.PhaseReady || snap.Running != challenge.RunningTrue {
		t.Fatalf("snapshot after rejection = %+v, want unchanged {ready true}", snap)
	}
}

func TestStart_HappyPath(t *testing.T) {
	h := newHarness(t)
	h.state.MarkReady(false)

	if err := h.svc.Start(context.Background(), false, "Steve"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if len(h.gate.evacuateCalls) != 1 || h.gate.evacuateCalls[0] != "reset" {
		t.Errorf("evacuateCalls = %v, want [reset]", h.gate.evacuateCalls)
	}
	if h.process.stopCalls != 1 {
		t.Errorf("stopCalls = %d, want 1", h.process.stopCalls)
	}
	if h.world.wipeCalls != 1 || h.world.ensureCalls != 1 {
		t.Errorf("wipeCalls=%d ensureCalls=%d, want 1 each", h.world.wipeCalls, h.world.ensureCalls)
	}
	if h.process.startCalls != 1 {
		t.Errorf("startCalls = %d, want 1", h.process.startCalls)
	}
	if h.ready.drained != 1 {
		t.Errorf("DrainReady calls = %d, want 1", h.ready.drained)
	}
	if h.gate.hardcoreReadyCalls != 1 {
		t.Errorf("hardcoreReadyCalls = %d, want 1", h.gate.hardcoreReadyCalls)
	}
}

func TestStart_ForceOverridesRunningCheckAndUsesForceReason(t *testing.T) {
	h := newHarness(t)
	h.state.MarkReady(true)

	if err := h.svc.Start(context.Background(), true, "OP"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if len(h.gate.rejectedCalls) != 0 {
		t.Fatalf("force=true must not be rejected, got %+v", h.gate.rejectedCalls)
	}
	if len(h.gate.evacuateCalls) != 1 || h.gate.evacuateCalls[0] != "force-reset" {
		t.Errorf("evacuateCalls = %v, want [force-reset]", h.gate.evacuateCalls)
	}
}

func TestStart_EvacuateFailureRevertsState(t *testing.T) {
	h := newHarness(t)
	h.state.MarkReady(false)
	prior := h.state.Snapshot()
	h.gate.evacuateErr = errors.New("gate unreachable")

	if err := h.svc.Start(context.Background(), false, "OP"); err == nil {
		t.Fatal("expected an error when evacuate fails")
	}
	if h.process.stopCalls != 0 {
		t.Error("process must not be touched if evacuate never completed")
	}
	if snap := h.state.Snapshot(); snap != prior {
		t.Fatalf("snapshot after evacuate failure = %+v, want reverted to %+v", snap, prior)
	}
}

func TestStart_StopFailureMarksUnknownButStaysStarting(t *testing.T) {
	h := newHarness(t)
	h.state.MarkReady(false)
	h.process.stopErr = errors.New("signal failed")

	if err := h.svc.Start(context.Background(), false, "OP"); err == nil {
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

func TestStart_PrepareWorldFailureMarksStopped(t *testing.T) {
	h := newHarness(t)
	h.state.MarkReady(false)
	h.world.wipeErr = errors.New("disk full")

	if err := h.svc.Start(context.Background(), false, "OP"); err == nil {
		t.Fatal("expected an error when world prep fails")
	}
	if h.process.startCalls != 0 {
		t.Error("process must not be started if world prep failed")
	}
	if snap := h.state.Snapshot(); snap.Phase != challenge.PhaseStopped || snap.Running != challenge.RunningFalse {
		t.Fatalf("snapshot = %+v, want {stopped false}", snap)
	}
}

func TestStart_ProcessStartFailureMarksStopped(t *testing.T) {
	h := newHarness(t)
	h.state.MarkReady(false)
	h.process.startErr = errors.New("exec: not found")

	if err := h.svc.Start(context.Background(), false, "OP"); err == nil {
		t.Fatal("expected an error when process.Start fails")
	}
	if snap := h.state.Snapshot(); snap.Phase != challenge.PhaseStopped || snap.Running != challenge.RunningFalse {
		t.Fatalf("snapshot = %+v, want {stopped false}", snap)
	}
}

func TestStart_ReadyTimeoutMarksUnknownButStaysStarting(t *testing.T) {
	h := newHarness(t)
	h.state.MarkReady(false)
	h.ready.err = context.DeadlineExceeded

	if err := h.svc.Start(context.Background(), false, "OP"); err == nil {
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

func TestOpMutex_SerializesArchiveRequestAndStart(t *testing.T) {
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
		h.svc.HandleArchiveRequest("manualsave", 1)
	}()

	select {
	case <-blockArchive:
	case <-time.After(2 * time.Second):
		t.Fatal("HandleArchiveRequest never entered Save")
	}

	startDone := make(chan error, 1)
	go func() {
		startDone <- h.svc.Start(context.Background(), false, "OP")
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

func TestHandleDisconnect_MarksUnknown(t *testing.T) {
	h := newHarness(t)
	h.state.MarkReady(true)
	h.svc.HandleDisconnect()
	if snap := h.state.Snapshot(); snap.Running != challenge.RunningUnknown {
		t.Fatalf("running = %v, want unknown", snap.Running)
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
