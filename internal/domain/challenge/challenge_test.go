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
		current Running
		force   bool
		wantOK  bool
	}{
		{"not running, no force -> allowed", RunningFalse, false, true},
		{"running, no force -> rejected", RunningTrue, false, false},
		{"unknown, no force -> rejected", RunningUnknown, false, false},
		{"running, force -> allowed", RunningTrue, true, true},
		{"unknown, force -> allowed", RunningUnknown, true, true},
		{"not running, force -> allowed", RunningFalse, true, true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ok, reason := DecideStart(c.current, c.force)
			if ok != c.wantOK {
				t.Fatalf("DecideStart(%v, %v) ok = %v, want %v", c.current, c.force, ok, c.wantOK)
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
