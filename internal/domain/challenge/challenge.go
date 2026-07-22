// Package challenge holds the pure vocabulary and rules for hardcore's
// process-lifecycle/running state (spec 3.1節). Nothing here does I/O or
// holds mutable state — that's port.ChallengeStateRepository's job,
// implemented by adapter/fsstate. This package only decides what the
// rules say, never how they're stored or made atomic.
package challenge

// Phase is the process-lifecycle 4-value state (spec 3.1節). Stopping was
// added alongside PhaseStarting when /deactivate was introduced: both are
// "a transition is in flight" states that block every other operation
// uniformly (spec 2.1節「処理中です」), regardless of clean/force.
type Phase string

const (
	PhaseStopped  Phase = "stopped"
	PhaseStarting Phase = "starting"
	PhaseReady    Phase = "ready"
	PhaseStopping Phase = "stopping"
)

// inTransition reports whether phase is a mid-transition state that
// uniformly blocks /start·/start clean·/load·/deactivate regardless of
// clean/force (spec 2.1節).
func inTransition(phase Phase) bool {
	return phase == PhaseStarting || phase == PhaseStopping
}

const reasonInTransition = "処理中です。しばらくお待ちください"

// Running is the challenge-in-progress cache. Unknown is the safe default
// used whenever Manager cannot be sure the hardcore process is alive but
// disconnected (startup, or MOD⇔Manager disconnect) and is treated the
// same as true by DecideStart (spec 3.1節・7節). Unlike Phase, Running is
// persisted across Manager restarts by adapter/fsstate — see that
// package's doc comment for why this was necessary to break the initial
// /start deadlock (architecture-manager.md 2節).
type Running string

const (
	RunningTrue    Running = "true"
	RunningFalse   Running = "false"
	RunningUnknown Running = "unknown"
)

// RunningFromBool maps the hardcore MOD's boolean running value
// (docs/protocol-mod-manager.md 3.1節・3.2節) onto the tri-state Running.
func RunningFromBool(running bool) Running {
	if running {
		return RunningTrue
	}
	return RunningFalse
}

// State is an immutable copy of {phase, running} at one instant.
type State struct {
	Phase   Phase
	Running Running
}

// DecideStart is the pure "should /start clean·/load be allowed right now"
// rule (spec 2.1節「forceの適用範囲」・3.1節). A mid-transition phase always
// rejects regardless of force (spec 2.1節「処理中です」); otherwise force
// skips the running check unconditionally, and a challenge already running
// (or of unknown status) is rejected.
//
// This is deliberately just a decision, not a transition:
// port.ChallengeStateRepository combines it with the actual {phase, running}
// mutation inside one atomic operation (TryMarkStarting), so two concurrent
// callers can't both observe "allowed" before either commits — see that
// port's doc comment.
func DecideStart(phase Phase, current Running, force bool) (ok bool, rejectReason string) {
	if inTransition(phase) {
		return false, reasonInTransition
	}
	if !force && current != RunningFalse {
		return false, "挑戦が進行中です"
	}
	return true, ""
}

// DecideResume is the pure "should /start（clean無し）be allowed right now"
// rule (spec 2.1節・7.3節). Unlike DecideStart, it never looks at Running —
// only whether a process is already running (phase==ready) or a transition
// is already in flight. This phase-only judgement is the structural fix for
// the initial /start deadlock: it never depends on a Running value that can
// be unknown (architecture-manager.md 8a節).
func DecideResume(phase Phase) (ok bool, rejectReason string) {
	switch phase {
	case PhaseStopped:
		return true, ""
	case PhaseReady:
		return false, "既に起動しています"
	default: // starting, stopping
		return false, reasonInTransition
	}
}

// DecideDeactivate is the pure "should /deactivate be allowed right now"
// rule (spec 2.1節・7.4節). Only a running process (phase==ready) may be
// deactivated; already-stopped or mid-transition phases are rejected.
func DecideDeactivate(phase Phase) (ok bool, rejectReason string) {
	switch phase {
	case PhaseReady:
		return true, ""
	case PhaseStopped:
		return false, "既に停止しています"
	default: // starting, stopping
		return false, reasonInTransition
	}
}
