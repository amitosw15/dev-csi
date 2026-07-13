package storage

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	TaskStateActive = 1
	TaskStateDone   = 2
	TaskStateFailed = 4

	DefaultPoolSizeMiB int64 = 10 * 1024 * 1024 // 10 TiB
)

// FakeVolume represents a volume in the fake storage array.
type FakeVolume struct {
	Name     string
	WWN      string // 32-char uppercase hex, e.g. "60002AC000000000000000010000B5D6"
	UUID     string // e.g. "550e8400-e29b-41d4-a716-446655440000"
	SizeMiB  int64  // MiB (3PAR convention)
	IsSnap   bool   // true if this is a snapshot/virtual copy
	ParentName string // source volume name, if IsSnap
}

// Task represents an async operation (e.g. promote virtual copy).
type Task struct {
	ID    int
	State int32 // TaskStateActive / TaskStateDone / TaskStateFailed
}

// State is the single source of truth for the DevStorage fake array.
type State struct {
	mu       sync.RWMutex
	volumes  map[string]*FakeVolume // name → volume
	hosts    map[string][]string    // hostName → []IQN
	hostSets map[string][]string    // hostSetName → []hostName
	vluns    map[string][]vlunEntry // lunName → []vlunEntry
	sessions map[string]struct{}    // active session keys
	tasks    map[int]*Task

	poolSizeMiB     int64
	allocatedMiB    int64
	nextTaskID      int
}

type vlunEntry struct {
	HostSetName string
	LunID       int
}

// NewState initialises the state with a pool size and optional seed volumes.
func NewState(poolSizeMiB int64, seed []FakeVolume) *State {
	if poolSizeMiB <= 0 {
		poolSizeMiB = DefaultPoolSizeMiB
	}
	s := &State{
		volumes:     make(map[string]*FakeVolume),
		hosts:       make(map[string][]string),
		hostSets:    make(map[string][]string),
		vluns:       make(map[string][]vlunEntry),
		sessions:    make(map[string]struct{}),
		tasks:       make(map[int]*Task),
		poolSizeMiB: poolSizeMiB,
		nextTaskID:  1,
	}
	for i := range seed {
		v := seed[i]
		s.volumes[v.Name] = &v
		s.allocatedMiB += v.SizeMiB
	}
	return s
}

// --- Sessions ---

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

// --- Volumes ---

// CreateVolume allocates a new volume. SizeMiB=0 defaults to 1024 MiB.
func (s *State) CreateVolume(name string, sizeMiB int64) (*FakeVolume, error) {
	if sizeMiB <= 0 {
		sizeMiB = 1024
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.volumes[name]; exists {
		// Idempotent: return existing.
		v := s.volumes[name]
		return v, nil
	}
	if s.allocatedMiB+sizeMiB > s.poolSizeMiB {
		return nil, fmt.Errorf("insufficient capacity: need %d MiB, have %d MiB free",
			sizeMiB, s.poolSizeMiB-s.allocatedMiB)
	}

	v := &FakeVolume{
		Name:    name,
		WWN:     wwnFromName(name),
		UUID:    uuidFromName(name),
		SizeMiB: sizeMiB,
	}
	s.volumes[name] = v
	s.allocatedMiB += sizeMiB
	return v, nil
}

// CreateSnapshot creates a snapshot of src named dst.
func (s *State) CreateSnapshot(srcName, snapName string) (*FakeVolume, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	src, ok := s.volumes[srcName]
	if !ok {
		return nil, fmt.Errorf("source volume %q not found", srcName)
	}
	if _, exists := s.volumes[snapName]; exists {
		return s.volumes[snapName], nil
	}
	snap := &FakeVolume{
		Name:       snapName,
		WWN:        wwnFromName(snapName),
		UUID:       uuidFromName(snapName),
		SizeMiB:    src.SizeMiB,
		IsSnap:     true,
		ParentName: srcName,
	}
	s.volumes[snapName] = snap
	// Snapshots don't consume additional pool space in this simple model.
	return snap, nil
}

// RenameVolume renames a volume.
func (s *State) RenameVolume(oldName, newName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.volumes[oldName]
	if !ok {
		return fmt.Errorf("volume %q not found", oldName)
	}
	v.Name = newName
	s.volumes[newName] = v
	delete(s.volumes, oldName)
	return nil
}

// DeleteVolume removes a volume and frees its capacity.
func (s *State) DeleteVolume(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.volumes[name]
	if !ok {
		return nil // idempotent
	}
	if !v.IsSnap {
		s.allocatedMiB -= v.SizeMiB
		if s.allocatedMiB < 0 {
			s.allocatedMiB = 0
		}
	}
	delete(s.volumes, name)
	return nil
}

func (s *State) GetVolume(name string) (*FakeVolume, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.volumes[name]
	return v, ok
}

func (s *State) FindVolumeByWWN(wwn string) (*FakeVolume, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, v := range s.volumes {
		if equalWWN(v.WWN, wwn) {
			return v, true
		}
	}
	return nil, false
}

func (s *State) FindVolumeByUUID(uuid string) (*FakeVolume, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, v := range s.volumes {
		if equalCI(v.UUID, uuid) {
			return v, true
		}
	}
	return nil, false
}

func (s *State) ListVolumes() []*FakeVolume {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*FakeVolume, 0, len(s.volumes))
	for _, v := range s.volumes {
		out = append(out, v)
	}
	return out
}

// --- Capacity ---

func (s *State) PoolSizeMiB() int64 { return s.poolSizeMiB }

func (s *State) FreeMiB() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.poolSizeMiB - s.allocatedMiB
}

func (s *State) AllocatedMiB() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.allocatedMiB
}

// --- Tasks ---

// StartTask creates a task that transitions from ACTIVE → DONE after a
// random delay between minMs and maxMs milliseconds.
func (s *State) StartTask(minMs, maxMs int) *Task {
	s.mu.Lock()
	id := s.nextTaskID
	s.nextTaskID++
	t := &Task{ID: id, State: TaskStateActive}
	s.tasks[id] = t
	s.mu.Unlock()

	span := maxMs - minMs
	if span <= 0 {
		span = 1
	}
	delay := minMs + randInt(span)
	go func() {
		time.Sleep(time.Duration(delay) * time.Millisecond)
		atomic.StoreInt32(&t.State, TaskStateDone)
	}()
	return t
}

func (s *State) GetTask(id int) (*Task, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.tasks[id]
	return t, ok
}

// --- Hosts / HostSets ---

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
	n := min(8, len(iqn))
	name := "fake-host-" + iqn[:n]
	s.hosts[name] = []string{iqn}
	return name
}

func (s *State) GetHostByIQN(iqn string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for name, iqns := range s.hosts {
		for _, q := range iqns {
			if equalCI(q, iqn) {
				return name, true
			}
		}
	}
	return "", false
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

// --- VLUNs ---

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

func (s *State) GetVlun(volumeName, hostSetName string) (vlunEntry, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, e := range s.vluns[volumeName] {
		if e.HostSetName == hostSetName {
			return e, true
		}
	}
	return vlunEntry{}, false
}

func (s *State) ListVluns() []map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []map[string]any
	for volName, entries := range s.vluns {
		for _, e := range entries {
			result = append(result, map[string]any{
				"volumeName": volName,
				"hostname":   e.HostSetName,
				"lun":        e.LunID,
				"remoteName": e.HostSetName,
				"type":       5,
			})
		}
	}
	return result
}

// --- WWN / UUID generation ---

// wwnFromName generates a deterministic 32-char hex WWN from the volume name.
// Format mirrors a real 3PAR WWN: starts with "6" (NAA IEEE registered extended).
func wwnFromName(name string) string {
	h := sha256.Sum256([]byte("wwn:" + name))
	raw := hex.EncodeToString(h[:16])
	// Force NAA type 6 prefix.
	return "6" + strings.ToUpper(raw[1:])
}

// uuidFromName generates a deterministic UUID (v4-format) from the volume name.
func uuidFromName(name string) string {
	h := sha256.Sum256([]byte("uuid:" + name))
	// Format: xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx
	b := h[:]
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant bits
	return fmt.Sprintf("%x-%x-%x-%x-%x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// --- helpers ---

func equalWWN(a, b string) bool { return equalCI(norm(a), norm(b)) }

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

// randInt is a crypto-safe random int in [0, n).
func randInt(n int) int {
	max := big.NewInt(int64(n))
	v, _ := rand.Int(rand.Reader, max)
	return int(v.Int64())
}
