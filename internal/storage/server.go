// Package storage provides an HTTP server that mimics the HPE Primera/3PAR WSAPI.
// This allows the Forklift xcopy populator and CSI import plugin to run against
// a fake storage array without real hardware.
package storage

import (
	"encoding/json"
	"net/http"
	"strings"

	"k8s.io/klog/v2"
)

// NewServer returns an http.Handler implementing the minimal WSAPI surface
// needed by the Forklift xcopy populator and HpeImporter CSI import plugin.
func NewServer(state *State) http.Handler {
	mux := http.NewServeMux()

	// Session auth
	mux.HandleFunc("/api/v1/credentials", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			handleCreateSession(w, r, state)
		default:
			http.NotFound(w, r)
		}
	})
	mux.HandleFunc("/api/v1/credentials/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			key := strings.TrimPrefix(r.URL.Path, "/api/v1/credentials/")
			state.DeleteSessionKey(key)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.NotFound(w, r)
	})

	// Volumes (CSI import: lookup by WWN/UUID; xcopy: lookup by name)
	mux.HandleFunc("/api/v1/volumes", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			handleListVolumes(w, r, state)
			return
		}
		http.NotFound(w, r)
	})
	mux.HandleFunc("/api/v1/volumes/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			name := strings.TrimPrefix(r.URL.Path, "/api/v1/volumes/")
			handleGetVolume(w, r, state, name)
			return
		}
		http.NotFound(w, r)
	})

	// Hosts (xcopy: EnsureClonnerIgroup creates hosts)
	mux.HandleFunc("/api/v1/hosts", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			handleCreateHost(w, r, state)
		case http.MethodGet:
			handleListHosts(w, r, state)
		default:
			http.NotFound(w, r)
		}
	})

	// Host sets (xcopy: grouping)
	mux.HandleFunc("/api/v1/hostsets", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			handleCreateHostSet(w, r, state)
			return
		}
		http.NotFound(w, r)
	})
	mux.HandleFunc("/api/v1/hostsets/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			name := strings.TrimPrefix(r.URL.Path, "/api/v1/hostsets/")
			handleAddHostToSet(w, r, state, name)
			return
		}
		http.NotFound(w, r)
	})

	// VLUNs (xcopy: Map/UnMap)
	mux.HandleFunc("/api/v1/vluns", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			handleCreateVlun(w, r, state)
		case http.MethodGet:
			handleListVluns(w, r, state)
		default:
			http.NotFound(w, r)
		}
	})
	mux.HandleFunc("/api/v1/vluns/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			handleDeleteVlun(w, r, state)
			return
		}
		http.NotFound(w, r)
	})

	return mux
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		klog.V(2).Infof("storage: JSON encode error: %v", err)
	}
}

func handleCreateSession(w http.ResponseWriter, _ *http.Request, s *State) {
	key := s.NewSessionKey()
	writeJSON(w, http.StatusCreated, map[string]string{"key": key})
}

// handleListVolumes handles GET /api/v1/volumes?query=...
// Supports queries: "wwn EQ <wwn>" and "uuid EQ <uuid>" (3PAR WSAPI format).
func handleListVolumes(w http.ResponseWriter, r *http.Request, s *State) {
	q := r.URL.Query().Get("query")
	q = strings.Trim(q, `"`)

	if q == "" {
		vols := s.ListVolumes()
		members := make([]map[string]any, 0, len(vols))
		for _, v := range vols {
			members = append(members, volumeToMap(v))
		}
		writeJSON(w, http.StatusOK, map[string]any{"total": len(members), "members": members})
		return
	}

	parts := strings.Fields(q) // e.g. ["wwn", "EQ", "60002AC..."]
	if len(parts) != 3 {
		writeJSON(w, http.StatusOK, map[string]any{"total": 0, "members": []any{}})
		return
	}
	field, val := strings.ToLower(parts[0]), parts[2]

	var found FakeVolume
	var ok bool
	switch field {
	case "wwn":
		found, ok = s.FindVolumeByWWN(val)
	case "uuid":
		found, ok = s.FindVolumeByUUID(val)
	}

	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"total": 0, "members": []any{}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"total":   1,
		"members": []any{volumeToMap(found)},
	})
}

func handleGetVolume(w http.ResponseWriter, _ *http.Request, s *State, name string) {
	v, ok := s.GetVolume(name)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"desc": "volume not found"})
		return
	}
	writeJSON(w, http.StatusOK, volumeToMap(v))
}

func volumeToMap(v FakeVolume) map[string]any {
	return map[string]any{
		"name":            v.Name,
		"wwn":             v.WWN,
		"uuid":            v.UUID,
		"sizeMiB":         v.Size / (1 << 20),
		"provisioningType": 1,
	}
}

func handleCreateHost(w http.ResponseWriter, r *http.Request, s *State) {
	var body struct {
		Name    string   `json:"name"`
		FCPaths []struct{ WWN string } `json:"FCPaths"`
		ISCSIPaths []struct{ Name string } `json:"iSCSIPaths"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var iqn string
	if len(body.ISCSIPaths) > 0 {
		iqn = body.ISCSIPaths[0].Name
	}
	name := s.EnsureHost(iqn)
	writeJSON(w, http.StatusCreated, map[string]string{"name": name})
}

func handleListHosts(w http.ResponseWriter, _ *http.Request, _ *State) {
	writeJSON(w, http.StatusOK, map[string]any{"total": 0, "members": []any{}})
}

func handleCreateHostSet(w http.ResponseWriter, r *http.Request, s *State) {
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.EnsureHostSet(body.Name)
	writeJSON(w, http.StatusCreated, map[string]string{"name": body.Name})
}

func handleAddHostToSet(w http.ResponseWriter, r *http.Request, s *State, setName string) {
	var body struct {
		SetMembers []string `json:"setMembers"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	for _, h := range body.SetMembers {
		s.AddHostToSet(setName, h)
	}
	w.WriteHeader(http.StatusOK)
}

func handleCreateVlun(w http.ResponseWriter, r *http.Request, s *State) {
	var body struct {
		VolumeName  string `json:"volumeName"`
		Hostname    string `json:"hostname"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	lunID := s.MapLUN(body.VolumeName, body.Hostname)
	writeJSON(w, http.StatusCreated, map[string]any{
		"volumeName": body.VolumeName,
		"hostname":   body.Hostname,
		"lun":        lunID,
	})
}

func handleDeleteVlun(w http.ResponseWriter, r *http.Request, s *State) {
	// path: /api/v1/vluns/<volumeName>,<lun>,<hostname>
	tail := strings.TrimPrefix(r.URL.Path, "/api/v1/vluns/")
	parts := strings.SplitN(tail, ",", 3)
	if len(parts) < 3 {
		http.Error(w, "bad vlun path", http.StatusBadRequest)
		return
	}
	s.UnmapLUN(parts[0], parts[2])
	w.WriteHeader(http.StatusNoContent)
}

func handleListVluns(w http.ResponseWriter, _ *http.Request, s *State) {
	entries := s.ListVluns()
	writeJSON(w, http.StatusOK, map[string]any{
		"total":   len(entries),
		"members": entries,
	})
}
