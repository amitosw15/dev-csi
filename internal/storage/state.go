package storage

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
)

// FakeVolume represents a volume in the fake array.
type FakeVolume struct {
	Name string
	WWN  string // uppercase hex, no prefix — e.g. "60002AC000000000000000010000B5D6"
	UUID string // e.g. "550e8400-e29b-41d4-a716-446655440000"
	Size int64  // bytes
}

// State is the shared in-memory store for the HTTP storage API.
type State struct {
	mu       sync.RWMutex
	volumes  map[string]FakeVolume  // name → volume
	hosts    map[string][]string    // hostName → []IQN
	hostSets map[string][]string    // hostSetName → []hostName
	vluns    map[string][]vlunEntry // lunName → []vlunEntry
	sessions map[string]struct{}    // active session keys
}

type vlunEntry struct {
	HostSetName string
	LunID       int
}

func NewState(seed []FakeVolume) *State {
	s := &State{
		volumes:  make(map[string]FakeVolume),
		hosts:    make(map[string][]string),
		hostSets: make(map[string][]string),
		vluns:    make(map[string][]vlunEntry),
		sessions: make(map[string]struct{}),
	}
	for _, v := range seed {
		s.volumes[v.Name] = v
	}
	return s
}

func (s *State) NewSessionKey() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	key := hex.EncodeToString(b)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[key] = struct{}{}
	return key
}

func (s *State) DeleteSessionKey(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, key)
}

// FindVolumeByWWN returns the volume whose WWN matches (case-insensitive prefix).
func (s *State) FindVolumeByWWN(wwn string) (FakeVolume, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, v := range s.volumes {
		if equalWWN(v.WWN, wwn) {
			return v, true
		}
	}
	return FakeVolume{}, false
}

// FindVolumeByUUID returns the volume whose UUID matches.
func (s *State) FindVolumeByUUID(uuid string) (FakeVolume, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, v := range s.volumes {
		if equalCI(v.UUID, uuid) {
			return v, true
		}
	}
	return FakeVolume{}, false
}

func (s *State) GetVolume(name string) (FakeVolume, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.volumes[name]
	return v, ok
}

func (s *State) ListVolumes() []FakeVolume {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]FakeVolume, 0, len(s.volumes))
	for _, v := range s.volumes {
		out = append(out, v)
	}
	return out
}

// EnsureHost creates the host if it doesn't exist, returning its name.
func (s *State) EnsureHost(iqn string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for name, iqns := range s.hosts {
		for _, q := range iqns {
			if equalCI(q, iqn) {
				return name
			}
		}
	}
	name := "fake-host-" + iqn[:min(8, len(iqn))]
	s.hosts[name] = []string{iqn}
	return name
}

func (s *State) EnsureHostSet(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.hostSets[name]; !ok {
		s.hostSets[name] = []string{}
	}
}

func (s *State) AddHostToSet(setName, hostName string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, h := range s.hostSets[setName] {
		if h == hostName {
			return
		}
	}
	s.hostSets[setName] = append(s.hostSets[setName], hostName)
}

// MapLUN records a VLUN mapping and returns a fake LUN ID.
func (s *State) MapLUN(volumeName, hostSetName string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries := s.vluns[volumeName]
	for _, e := range entries {
		if e.HostSetName == hostSetName {
			return e.LunID
		}
	}
	id := len(entries) + 1
	s.vluns[volumeName] = append(entries, vlunEntry{HostSetName: hostSetName, LunID: id})
	return id
}

// UnmapLUN removes a VLUN mapping.
func (s *State) UnmapLUN(volumeName, hostSetName string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries := s.vluns[volumeName]
	out := entries[:0]
	for _, e := range entries {
		if e.HostSetName != hostSetName {
			out = append(out, e)
		}
	}
	s.vluns[volumeName] = out
}

// ListVluns returns all VLUNs.
func (s *State) ListVluns() []map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []map[string]any
	for volName, entries := range s.vluns {
		for _, e := range entries {
			result = append(result, map[string]any{
				"volumeName":  volName,
				"hostname":    e.HostSetName,
				"lun":         e.LunID,
				"remoteName":  e.HostSetName,
				"type":        5, // 3PAR type 5 = host set
			})
		}
	}
	return result
}

func equalWWN(a, b string) bool {
	return equalCI(norm(a), norm(b))
}

func equalCI(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		ca, cb := a[i], b[i]
		if ca >= 'A' && ca <= 'Z' {
			ca += 32
		}
		if cb >= 'A' && cb <= 'Z' {
			cb += 32
		}
		if ca != cb {
			return false
		}
	}
	return true
}

func norm(s string) string {
	// strip common prefixes from WWN strings
	for _, p := range []string{"naa.", "NAA.", "vml.", "VML."} {
		if len(s) > len(p) && equalCI(s[:len(p)], p) {
			return s[len(p):]
		}
	}
	return s
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
