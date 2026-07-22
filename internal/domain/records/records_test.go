package records

import (
	"testing"
	"time"
)

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	tm, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return tm
}

func TestAggregateSaveData_Empty(t *testing.T) {
	if got := AggregateSaveData(nil); len(got) != 0 {
		t.Errorf("got %v, want empty", got)
	}
}

func TestAggregateSaveData_FlattensAndSortsByTimestamp(t *testing.T) {
	a := ChallengeRecord{
		ChallengeID: "A",
		Events: []Event{
			{Type: EventSave, ElapsedTime: 600, Timestamp: mustTime(t, "2026-07-18T12:00:00Z"), ArchiveName: "save1", Trigger: &Trigger{Kind: "boss", MobID: "twilightforest:naga"}},
			{Type: EventDeath, ElapsedTime: 900, Timestamp: mustTime(t, "2026-07-18T12:05:00Z"), DeadPlayer: &PlayerRef{UUID: "u1", Name: "Steve"}, KillLog: "Steve was slain by Zombie"},
		},
	}
	b := ChallengeRecord{
		ChallengeID: "B",
		Events: []Event{
			{Type: EventClear, ElapsedTime: 100, Timestamp: mustTime(t, "2026-07-17T00:00:00Z"), Trigger: &Trigger{Kind: "boss", MobID: "twilightforest:ur_ghast"}},
		},
	}

	entries := AggregateSaveData([]ChallengeRecord{a, b})
	if len(entries) != 3 {
		t.Fatalf("len(entries) = %d, want 3", len(entries))
	}
	if entries[0].ChallengeID != "B" || entries[0].Type != EventClear {
		t.Errorf("entries[0] = %+v, want B's clear event first (oldest)", entries[0])
	}
	if entries[1].ChallengeID != "A" || entries[1].Type != EventSave {
		t.Errorf("entries[1] = %+v, want A's save event second", entries[1])
	}
	if entries[2].ChallengeID != "A" || entries[2].Type != EventDeath {
		t.Errorf("entries[2] = %+v, want A's death event third", entries[2])
	}
	if entries[1].ArchiveName != "save1" {
		t.Errorf("entries[1].ArchiveName = %q, want save1", entries[1].ArchiveName)
	}
	if entries[2].DeadPlayer == nil || entries[2].DeadPlayer.Name != "Steve" {
		t.Errorf("entries[2].DeadPlayer = %+v, want Steve", entries[2].DeadPlayer)
	}
}

func TestAggregateSenpan_TalliesDeathsAcrossChallenges(t *testing.T) {
	a := ChallengeRecord{ChallengeID: "A", Events: []Event{
		{Type: EventDeath, Timestamp: mustTime(t, "2026-01-01T00:00:00Z"), DeadPlayer: &PlayerRef{UUID: "u1", Name: "Steve"}},
		{Type: EventSave, Timestamp: mustTime(t, "2026-01-01T00:01:00Z")},
	}}
	b := ChallengeRecord{ChallengeID: "B", Events: []Event{
		{Type: EventDeath, Timestamp: mustTime(t, "2026-01-02T00:00:00Z"), DeadPlayer: &PlayerRef{UUID: "u1", Name: "Steve"}},
		{Type: EventDeath, Timestamp: mustTime(t, "2026-01-02T00:01:00Z"), DeadPlayer: &PlayerRef{UUID: "u2", Name: "Alex"}},
	}}

	entries := AggregateSenpan([]ChallengeRecord{a, b})
	if len(entries) != 2 {
		t.Fatalf("len(entries) = %d, want 2", len(entries))
	}
	if entries[0].Player.Name != "Steve" || entries[0].Count != 2 {
		t.Errorf("entries[0] = %+v, want Steve:2 (most deaths first)", entries[0])
	}
	if entries[1].Player.Name != "Alex" || entries[1].Count != 1 {
		t.Errorf("entries[1] = %+v, want Alex:1", entries[1])
	}
}

func TestAggregateSenpan_TiesBrokenByName(t *testing.T) {
	a := ChallengeRecord{ChallengeID: "A", Events: []Event{
		{Type: EventDeath, Timestamp: mustTime(t, "2026-01-01T00:00:00Z"), DeadPlayer: &PlayerRef{UUID: "u2", Name: "Zed"}},
		{Type: EventDeath, Timestamp: mustTime(t, "2026-01-01T00:01:00Z"), DeadPlayer: &PlayerRef{UUID: "u1", Name: "Alex"}},
	}}

	entries := AggregateSenpan([]ChallengeRecord{a})
	if len(entries) != 2 || entries[0].Player.Name != "Alex" || entries[1].Player.Name != "Zed" {
		t.Fatalf("entries = %+v, want [Alex, Zed] (alphabetical tiebreak)", entries)
	}
}

func TestAggregateSenpan_IgnoresNonDeathEvents(t *testing.T) {
	a := ChallengeRecord{ChallengeID: "A", Events: []Event{
		{Type: EventSave, Timestamp: mustTime(t, "2026-01-01T00:00:00Z")},
		{Type: EventClear, Timestamp: mustTime(t, "2026-01-01T00:01:00Z")},
	}}

	if entries := AggregateSenpan([]ChallengeRecord{a}); len(entries) != 0 {
		t.Errorf("entries = %v, want empty (no death events)", entries)
	}
}
