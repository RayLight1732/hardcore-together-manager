package challenge

import "testing"

func TestRunningFromBool(t *testing.T) {
	if got := RunningFromBool(true); got != RunningTrue {
		t.Errorf("RunningFromBool(true) = %v, want RunningTrue", got)
	}
	if got := RunningFromBool(false); got != RunningFalse {
		t.Errorf("RunningFromBool(false) = %v, want RunningFalse", got)
	}
}

func TestDecideStart(t *testing.T) {
	cases := []struct {
		name    string
		phase   Phase
		current Running
		force   bool
		wantOK  bool
	}{
		{"stopped, not running, no force -> allowed", PhaseStopped, RunningFalse, false, true},
		{"stopped, running, no force -> rejected", PhaseStopped, RunningTrue, false, false},
		{"stopped, unknown, no force -> rejected", PhaseStopped, RunningUnknown, false, false},
		{"stopped, running, force -> allowed", PhaseStopped, RunningTrue, true, true},
		{"stopped, unknown, force -> allowed", PhaseStopped, RunningUnknown, true, true},
		{"stopped, not running, force -> allowed", PhaseStopped, RunningFalse, true, true},
		{"ready, not running, no force -> allowed", PhaseReady, RunningFalse, false, true},
		{"starting, not running, force -> rejected (mid-transition wins)", PhaseStarting, RunningFalse, true, false},
		{"stopping, not running, force -> rejected (mid-transition wins)", PhaseStopping, RunningFalse, true, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ok, reason := DecideStart(c.phase, c.current, c.force)
			if ok != c.wantOK {
				t.Fatalf("DecideStart(%v, %v, %v) ok = %v, want %v", c.phase, c.current, c.force, ok, c.wantOK)
			}
			if !ok && reason == "" {
				t.Error("expected a non-empty reject reason")
			}
			if ok && reason != "" {
				t.Errorf("expected no reject reason when allowed, got %q", reason)
			}
		})
	}
}

func TestDecideResume(t *testing.T) {
	cases := []struct {
		phase  Phase
		wantOK bool
	}{
		{PhaseStopped, true},
		{PhaseReady, false},
		{PhaseStarting, false},
		{PhaseStopping, false},
	}

	for _, c := range cases {
		t.Run(string(c.phase), func(t *testing.T) {
			ok, reason := DecideResume(c.phase)
			if ok != c.wantOK {
				t.Fatalf("DecideResume(%v) ok = %v, want %v", c.phase, ok, c.wantOK)
			}
			if !ok && reason == "" {
				t.Error("expected a non-empty reject reason")
			}
		})
	}
}

func TestDecideResume_NeverLooksAtRunning(t *testing.T) {
	// DecideResume takes no Running argument at all — this test exists
	// mostly as documentation: the whole point of /start（clean無し）is
	// that its accept/reject decision cannot depend on a value that can be
	// unknown (architecture-manager.md 8a節).
	ok, _ := DecideResume(PhaseStopped)
	if !ok {
		t.Fatal("expected stopped to be allowed regardless of any running value")
	}
}

func TestDecideDeactivate(t *testing.T) {
	cases := []struct {
		phase  Phase
		wantOK bool
	}{
		{PhaseReady, true},
		{PhaseStopped, false},
		{PhaseStarting, false},
		{PhaseStopping, false},
	}

	for _, c := range cases {
		t.Run(string(c.phase), func(t *testing.T) {
			ok, reason := DecideDeactivate(c.phase)
			if ok != c.wantOK {
				t.Fatalf("DecideDeactivate(%v) ok = %v, want %v", c.phase, ok, c.wantOK)
			}
			if !ok && reason == "" {
				t.Error("expected a non-empty reject reason")
			}
		})
	}
}
