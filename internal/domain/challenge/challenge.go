// Package challenge holds the pure vocabulary and rules for hardcore's
// process-lifecycle/running state (spec 3.1節). Nothing here does I/O or
// holds mutable state — that's port.ChallengeStateRepository's job,
// implemented by adapter/memstate. This package only decides what the
// rules say, never how they're stored or made atomic.
package challenge

// Phase is the process-lifecycle 3-value state (spec 3.1節).
type Phase string

const (
	PhaseStopped  Phase = "stopped"
	PhaseStarting Phase = "starting"
	PhaseReady    Phase = "ready"
)

// Running is the challenge-in-progress cache. Unknown is the safe default
// used whenever Manager cannot be sure (startup, or MOD⇔Manager disconnect)
// and is treated the same as true by DecideStart (spec 3.1節・7節).
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

// DecideStart is the pure "should /start·/load be allowed right now" rule
// (spec 2.1節「forceの適用範囲」・3.1節). force skips the check
// unconditionally; otherwise a challenge already running (or of unknown
// status) is rejected.
//
// This is deliberately just a decision, not a transition:
// port.ChallengeStateRepository combines it with the actual {phase, running}
// mutation inside one atomic operation (TryMarkStarting), so two concurrent
// callers can't both observe "allowed" before either commits — see that
// port's doc comment.
func DecideStart(current Running, force bool) (ok bool, rejectReason string) {
	if !force && current != RunningFalse {
		return false, "挑戦が進行中です"
	}
	return true, ""
}
