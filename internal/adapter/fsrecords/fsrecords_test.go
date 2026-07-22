package fsrecords

import (
	"os"
	"path/filepath"
	"testing"
)

func writeRecord(t *testing.T, dir, challengeID, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, challengeID+".json")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReadAll_MissingDirIsNotError(t *testing.T) {
	r := New(filepath.Join(t.TempDir(), "records"))
	got, err := r.ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %v, want empty", got)
	}
}

func TestReadAll_ParsesEveryChallengeFile(t *testing.T) {
	dir := t.TempDir()
	writeRecord(t, dir, "A", `{
		"challengeId": "A",
		"lastKnownElapsedTime": 900,
		"events": [
			{"type":"save","elapsedTime":600,"timestamp":"2026-07-18T12:00:00Z","archiveName":"save1","trigger":{"kind":"boss","mobId":"twilightforest:naga"}},
			{"type":"death","elapsedTime":900,"timestamp":"2026-07-18T12:05:00Z","deadPlayer":{"uuid":"u1","name":"Steve"},"killLog":"Steve was slain by Zombie"}
		]
	}`)
	writeRecord(t, dir, "B", `{
		"challengeId": "B",
		"lastKnownElapsedTime": 100,
		"events": [
			{"type":"clear","elapsedTime":100,"timestamp":"2026-07-17T00:00:00Z","trigger":{"kind":"boss","mobId":"twilightforest:ur_ghast"}}
		]
	}`)

	r := New(dir)
	got, err := r.ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2", len(got))
	}

	byID := map[string][]int{}
	for i, rec := range got {
		byID[rec.ChallengeID] = append(byID[rec.ChallengeID], i)
	}
	if len(byID["A"]) != 1 || len(byID["B"]) != 1 {
		t.Fatalf("expected exactly one record each for A and B, got %v", byID)
	}
	a := got[byID["A"][0]]
	if len(a.Events) != 2 || a.LastKnownElapsedTime != 900 {
		t.Errorf("challenge A = %+v, unexpected content", a)
	}
}

func TestReadAll_SkipsMalformedFile(t *testing.T) {
	dir := t.TempDir()
	writeRecord(t, dir, "good", `{"challengeId":"good","lastKnownElapsedTime":0,"events":[
		{"type":"clear","elapsedTime":1,"timestamp":"2026-01-01T00:00:00Z"}
	]}`)
	writeRecord(t, dir, "bad", `{not valid json`)

	r := New(dir)
	got, err := r.ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1 (malformed file skipped)", len(got))
	}
	if got[0].ChallengeID != "good" {
		t.Errorf("got[0].ChallengeID = %q, want good", got[0].ChallengeID)
	}
}

func TestReadAll_IgnoresNonJSONFiles(t *testing.T) {
	dir := t.TempDir()
	writeRecord(t, dir, "good", `{"challengeId":"good","lastKnownElapsedTime":0,"events":[]}`)
	if err := os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("not a record"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}

	r := New(dir)
	got, err := r.ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1 (non-.json entries ignored)", len(got))
	}
}
