package archive

import (
	"testing"
	"time"
)

func TestDecideBaseName(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 34, 56, 0, time.UTC)

	if got := DecideBaseName("save1", now); got != "save1" {
		t.Errorf("DecideBaseName(manual) = %q, want save1", got)
	}
	if got, want := DecideBaseName("", now), "2026-07-18T12-34-56"; got != want {
		t.Errorf("DecideBaseName(auto) = %q, want %q", got, want)
	}
}

func TestDecideBaseName_NonUTCInputIsNormalized(t *testing.T) {
	jst := time.FixedZone("JST", 9*60*60)
	now := time.Date(2026, 7, 18, 21, 34, 56, 0, jst) // == 12:34:56 UTC
	if got, want := DecideBaseName("", now), "2026-07-18T12-34-56"; got != want {
		t.Errorf("DecideBaseName should normalize to UTC, got %q, want %q", got, want)
	}
}

func fakeExists(taken ...string) func(string) (bool, error) {
	set := make(map[string]bool, len(taken))
	for _, n := range taken {
		set[n] = true
	}
	return func(name string) (bool, error) { return set[name], nil }
}

func TestResolveName_ManualFreeName(t *testing.T) {
	got, err := ResolveName("save1", true, fakeExists())
	if err != nil {
		t.Fatalf("ResolveName: %v", err)
	}
	if got != "save1" {
		t.Errorf("got %q, want save1", got)
	}
}

func TestResolveName_ManualConflictRejects(t *testing.T) {
	_, err := ResolveName("save1", true, fakeExists("save1"))
	if err != ErrNameConflict {
		t.Fatalf("err = %v, want ErrNameConflict", err)
	}
}

func TestResolveName_AutoFreeName(t *testing.T) {
	got, err := ResolveName("2026-07-18T12-34-56", false, fakeExists())
	if err != nil {
		t.Fatalf("ResolveName: %v", err)
	}
	if got != "2026-07-18T12-34-56" {
		t.Errorf("got %q", got)
	}
}

func TestResolveName_AutoAppendsSuffixOnCollision(t *testing.T) {
	base := "2026-07-18T12-34-56"
	got, err := ResolveName(base, false, fakeExists(base, base+"-2"))
	if err != nil {
		t.Fatalf("ResolveName: %v", err)
	}
	if want := base + "-3"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolveName_PropagatesExistsError(t *testing.T) {
	boom := errTest("boom")
	_, err := ResolveName("save1", true, func(string) (bool, error) { return false, boom })
	if err != boom {
		t.Fatalf("err = %v, want %v", err, boom)
	}
}

type errTest string

func (e errTest) Error() string { return string(e) }
