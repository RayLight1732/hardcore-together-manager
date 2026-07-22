// Package records holds the pure vocabulary and aggregation rules for
// challenge event logs (spec 5.5節). Reading records/<challengeId>.json
// from disk is I/O and belongs to port.RecordsRepository, implemented by
// adapter/fsrecords; this package only aggregates already-in-memory
// ChallengeRecords for /savedata and /senpan.
package records

import (
	"sort"
	"time"
)

// EventType is one of the three event kinds a challenge log can contain
// (spec 5.5節).
type EventType string

const (
	EventSave  EventType = "save"
	EventDeath EventType = "death"
	EventClear EventType = "clear"
)

// PlayerRef identifies a player by UUID and (at-the-time) name.
type PlayerRef struct {
	UUID string `json:"uuid"`
	Name string `json:"name"`
}

// Trigger is the "what caused this save/clear" field: a boss kill (mobId) or
// a manual /archive (player, the executing OP's UUID).
type Trigger struct {
	Kind   string `json:"kind"`
	MobID  string `json:"mobId,omitempty"`
	Player string `json:"player,omitempty"`
}

// Event is one entry in a challenge's events array. Which optional fields
// are populated depends on Type (spec 5.5節).
type Event struct {
	Type        EventType `json:"type"`
	ElapsedTime int64     `json:"elapsedTime"`
	Timestamp   time.Time `json:"timestamp"`

	// save
	ArchiveName string   `json:"archiveName,omitempty"`
	Trigger     *Trigger `json:"trigger,omitempty"`

	// death
	DeadPlayer *PlayerRef `json:"deadPlayer,omitempty"`
	KillLog    string     `json:"killLog,omitempty"`
}

// ChallengeRecord is the full content of one records/<challengeId>.json file.
type ChallengeRecord struct {
	ChallengeID          string  `json:"challengeId"`
	LastKnownElapsedTime int64   `json:"lastKnownElapsedTime"`
	Events               []Event `json:"events"`
}

// SaveDataEntry is one row of the /savedata listing: an Event tagged with
// which challenge it belongs to (docs/protocol-gate-manager.md 3.6節).
type SaveDataEntry struct {
	ChallengeID string `json:"challengeId"`
	Event
}

// SenpanEntry is one player's death tally for /senpan (docs/protocol-gate-manager.md 3.7節).
type SenpanEntry struct {
	Player PlayerRef `json:"player"`
	Count  int       `json:"count"`
}

// AggregateSaveData returns every save/death/clear event across all given
// challenges, oldest first, for the /savedata command.
func AggregateSaveData(challenges []ChallengeRecord) []SaveDataEntry {
	entries := []SaveDataEntry{}
	for _, rec := range challenges {
		for _, ev := range rec.Events {
			entries = append(entries, SaveDataEntry{ChallengeID: rec.ChallengeID, Event: ev})
		}
	}
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].Timestamp.Before(entries[j].Timestamp)
	})
	return entries
}

// AggregateSenpan tallies `type: death` events by dead player across all
// given challenges, for /senpan list|count. Both modes use the same
// aggregation; only Gate's display format differs between them
// (docs/protocol-gate-manager.md 3.7節), so mode isn't a parameter here.
// Results are ordered by count descending, then name ascending, so callers
// get a deterministic ranking.
func AggregateSenpan(challenges []ChallengeRecord) []SenpanEntry {
	counts := make(map[string]*SenpanEntry)
	var order []string
	for _, rec := range challenges {
		for _, ev := range rec.Events {
			if ev.Type != EventDeath || ev.DeadPlayer == nil {
				continue
			}
			key := ev.DeadPlayer.UUID
			entry, ok := counts[key]
			if !ok {
				entry = &SenpanEntry{Player: *ev.DeadPlayer}
				counts[key] = entry
				order = append(order, key)
			}
			entry.Count++
		}
	}

	result := make([]SenpanEntry, 0, len(order))
	for _, key := range order {
		result = append(result, *counts[key])
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].Count != result[j].Count {
			return result[i].Count > result[j].Count
		}
		return result[i].Player.Name < result[j].Player.Name
	})
	return result
}
