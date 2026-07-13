package csi

import "sync"

// Volume is an in-memory representation of a provisioned CSI volume.
type Volume struct {
	ID            string
	Name          string
	CapacityBytes int64
	MountedAt     map[string]struct{}
}

// State is a thread-safe in-memory volume store.
type State struct {
	mu      sync.RWMutex
	volumes map[string]Volume
}

func NewState() *State {
	return &State{volumes: make(map[string]Volume)}
}

func (s *State) Add(v Volume) {
	if v.MountedAt == nil {
		v.MountedAt = make(map[string]struct{})
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.volumes[v.ID] = v
}

func (s *State) Get(id string) (Volume, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.volumes[id]
	return v, ok
}

func (s *State) GetByName(name string) (Volume, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, v := range s.volumes {
		if v.Name == name {
			return v, true
		}
	}
	return Volume{}, false
}

func (s *State) Delete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.volumes, id)
}

func (s *State) List() []Volume {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Volume, 0, len(s.volumes))
	for _, v := range s.volumes {
		out = append(out, v)
	}
	return out
}

func (s *State) AddMount(id, targetPath string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := s.volumes[id]; ok {
		if v.MountedAt == nil {
			v.MountedAt = make(map[string]struct{})
		}
		v.MountedAt[targetPath] = struct{}{}
		s.volumes[id] = v
	}
}

func (s *State) RemoveMount(id, targetPath string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := s.volumes[id]; ok {
		delete(v.MountedAt, targetPath)
		s.volumes[id] = v
	}
}

func (s *State) IsMounted(id, targetPath string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.volumes[id]
	if !ok {
		return false
	}
	_, mounted := v.MountedAt[targetPath]
	return mounted
}
