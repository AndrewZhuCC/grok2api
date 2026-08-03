package resinquality

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// PoolState is the sticky-pool quarantine / strike state.
type PoolState struct {
	SoftStrikes        int            `json:"softStrikes"`
	ErrorStrikes       int            `json:"errorStrikes"`
	QuarantinedUntil   float64        `json:"quarantinedUntil"`
	QuarantineActive   bool           `json:"quarantineActive"`
	LastReason         string         `json:"lastReason"`
	LastClassification string         `json:"lastClassification"`
	LastOutputTPS      float64        `json:"lastOutputTps"`
	LastExitIP         string         `json:"lastExitIp"`
	SuspectExitIPs     []string       `json:"suspectExitIps"`
	LastAction         map[string]any `json:"lastAction,omitempty"`
	LastObservedAt     float64        `json:"lastObservedAt"`
	LastProbeAt        float64        `json:"lastProbeAt"`
}

// Stats counters for UI.
type Stats struct {
	Passive map[string]int `json:"passive"`
	Active  map[string]int `json:"active"`
	Actions map[string]int `json:"actions"`
}

// Event is a recent guard event for the admin UI.
type Event struct {
	TS             float64 `json:"ts"`
	Event          string  `json:"event"`
	Reason         string  `json:"reason,omitempty"`
	Classification string  `json:"classification,omitempty"`
	OutputTPS      float64 `json:"outputTps,omitempty"`
	ExitIP         string  `json:"exitIp,omitempty"`
	Cleared        int     `json:"cleared,omitempty"`
}

// State is persisted JSON.
type State struct {
	Version           int       `json:"version"`
	Pool              PoolState `json:"pool"`
	Statistics        Stats     `json:"statistics"`
	Events            []Event   `json:"events"`
	LastPassivePollAt float64   `json:"lastPassivePollAt"`
	LastActiveCycleAt float64   `json:"lastActiveCycleAt"`
	StartedAt         float64   `json:"startedAt"`
	UpdatedAt         float64   `json:"updatedAt"`
}

func defaultState() State {
	now := float64(time.Now().Unix())
	return State{
		Version: 1,
		Pool:    PoolState{LastClassification: ClassHealthy, SuspectExitIPs: []string{}},
		Statistics: Stats{
			Passive: map[string]int{"total": 0, "healthy": 0, "soft": 0, "hard": 0, "ignored": 0},
			Active:  map[string]int{"total": 0, "healthy": 0, "soft": 0, "hard": 0, "error": 0},
			Actions: map[string]int{"quarantined": 0, "restored": 0, "reshuffles": 0, "targetedClears": 0},
		},
		Events:    []Event{},
		StartedAt: now,
		UpdatedAt: now,
	}
}

type stateStore struct {
	mu   sync.Mutex
	path string
	cur  State
}

func newStateStore(path string) *stateStore {
	s := &stateStore{path: path, cur: defaultState()}
	if path != "" {
		if data, err := os.ReadFile(path); err == nil {
			var loaded State
			if json.Unmarshal(data, &loaded) == nil && loaded.Version == 1 {
				s.cur = loaded
				s.ensureMaps()
			}
		}
	}
	return s
}

func (s *stateStore) ensureMaps() {
	if s.cur.Statistics.Passive == nil {
		s.cur.Statistics.Passive = map[string]int{}
	}
	if s.cur.Statistics.Active == nil {
		s.cur.Statistics.Active = map[string]int{}
	}
	if s.cur.Statistics.Actions == nil {
		s.cur.Statistics.Actions = map[string]int{}
	}
	if s.cur.Pool.SuspectExitIPs == nil {
		s.cur.Pool.SuspectExitIPs = []string{}
	}
	if s.cur.Events == nil {
		s.cur.Events = []Event{}
	}
}

func (s *stateStore) snapshot() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	// shallow copy maps for read
	out := s.cur
	out.Pool.SuspectExitIPs = append([]string(nil), s.cur.Pool.SuspectExitIPs...)
	out.Events = append([]Event(nil), s.cur.Events...)
	out.Statistics.Passive = copyIntMap(s.cur.Statistics.Passive)
	out.Statistics.Active = copyIntMap(s.cur.Statistics.Active)
	out.Statistics.Actions = copyIntMap(s.cur.Statistics.Actions)
	if s.cur.Pool.LastAction != nil {
		out.Pool.LastAction = copyAnyMap(s.cur.Pool.LastAction)
	}
	return out
}

func (s *stateStore) update(fn func(st *State)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureMaps()
	fn(&s.cur)
	s.cur.UpdatedAt = float64(time.Now().Unix())
	_ = s.saveLocked()
}

func (s *stateStore) saveLocked() error {
	if s.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s.cur, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (st *State) bump(group, field string, n int) {
	var m map[string]int
	switch group {
	case "passive":
		if st.Statistics.Passive == nil {
			st.Statistics.Passive = map[string]int{}
		}
		m = st.Statistics.Passive
	case "active":
		if st.Statistics.Active == nil {
			st.Statistics.Active = map[string]int{}
		}
		m = st.Statistics.Active
	case "actions":
		if st.Statistics.Actions == nil {
			st.Statistics.Actions = map[string]int{}
		}
		m = st.Statistics.Actions
	default:
		return
	}
	m[field] = m[field] + n
}

func (st *State) appendEvent(ev Event) {
	st.Events = append(st.Events, ev)
	if len(st.Events) > 200 {
		st.Events = st.Events[len(st.Events)-200:]
	}
}

func copyIntMap(in map[string]int) map[string]int {
	out := make(map[string]int, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func copyAnyMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
