// Package storage provides an HTTP server mimicking HPE Primera/3PAR WSAPI.
// This allows the Forklift xcopy populator and CSI import plugin to work
// against a fake storage array with zero code changes — just set STORAGE_HOSTNAME.
package storage

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"

	"k8s.io/klog/v2"
)

// NewServer returns an http.Handler implementing the Primera3Par WSAPI surface
// needed by the Forklift xcopy populator and HpeImporter CSI import plugin.
func NewServer(state *State) http.Handler {
	return logMiddleware(newServer(state))
}

func logMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		klog.V(2).Infof("storage API → %s %s", r.Method, r.URL.String())
		lw := &loggingResponseWriter{ResponseWriter: w, code: 200}
		next.ServeHTTP(lw, r)
		klog.V(2).Infof("storage API ← %s %s %d", r.Method, r.URL.Path, lw.code)
	})
}

type loggingResponseWriter struct {
	http.ResponseWriter
	code int
}

func (lw *loggingResponseWriter) WriteHeader(code int) {
	lw.code = code
	lw.ResponseWriter.WriteHeader(code)
}

func newServer(state *State) *http.ServeMux {
	mux := http.NewServeMux()

	// System info + capacity
	mux.HandleFunc("/api/v1/system", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			handleSystem(w, r, state)
			return
		}
		http.NotFound(w, r)
	})

	// Session auth
	mux.HandleFunc("/api/v1/credentials", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			key := state.NewSessionKey()
			writeJSON(w, http.StatusCreated, map[string]string{"key": key})
			return
		}
		http.NotFound(w, r)
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

	// Volumes — CRUD + snapshot/rename
	mux.HandleFunc("/api/v1/volumes", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			handleListVolumes(w, r, state)
		case http.MethodPost:
			handleCreateVolume(w, r, state)
		default:
			http.NotFound(w, r)
		}
	})
	mux.HandleFunc("/api/v1/volumes/", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/api/v1/volumes/")
		switch r.Method {
		case http.MethodGet:
			handleGetVolume(w, state, name)
		case http.MethodPost:
			handleVolumeAction(w, r, state, name)
		case http.MethodDelete:
			if err := state.DeleteVolume(name); err != nil {
				writeJSON(w, http.StatusInternalServerError, apiErr(err.Error()))
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case http.MethodPut:
			handleRenameVolume(w, r, state, name)
		default:
			http.NotFound(w, r)
		}
	})

	// Tasks
	mux.HandleFunc("/api/v1/tasks/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		idStr := strings.TrimPrefix(r.URL.Path, "/api/v1/tasks/")
		id, err := strconv.Atoi(idStr)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, apiErr("invalid task id"))
			return
		}
		t, ok := state.GetTask(id)
		if !ok {
			writeJSON(w, http.StatusNotFound, apiErr(fmt.Sprintf("task %d not found", id)))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id":     t.ID,
			"type":   1,
			"status": atomic.LoadInt32(&t.State),
		})
	})

	// Hosts
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
	mux.HandleFunc("/api/v1/hosts/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			name := strings.TrimPrefix(r.URL.Path, "/api/v1/hosts/")
			handleGetHost(w, state, name)
			return
		}
		http.NotFound(w, r)
	})

	// Host sets
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

	// VLUNs
	mux.HandleFunc("/api/v1/vluns", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			handleCreateVlun(w, r, state)
		case http.MethodGet:
			entries := state.ListVluns()
			writeJSON(w, http.StatusOK, map[string]any{"total": len(entries), "members": entries})
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

// --- System ---

func handleSystem(w http.ResponseWriter, _ *http.Request, s *State) {
	totalMiB := s.PoolSizeMiB()
	freeMiB := s.FreeMiB()
	writeJSON(w, http.StatusOK, map[string]any{
		"id":              "DevStorage-001",
		"name":            "DevStorage Fake Array",
		"model":           "DevArray 9000",
		"serialNumber":    "DEVSTG001",
		"systemVersion":   "4.0.0.0",
		"IPv4Addr":        "127.0.0.1",
		"totalCapacityMiB": totalMiB,
		"allocatedCapacityMiB": totalMiB - freeMiB,
		"freeCapacityMiB": freeMiB,
	})
}

// --- Volumes ---

// handleListVolumes handles GET /api/v1/volumes?query=...
// Supports 3PAR query syntax: `"wwn EQ <wwn>"` and `"uuid EQ <uuid>"`.
func handleListVolumes(w http.ResponseWriter, r *http.Request, s *State) {
	q := strings.Trim(r.URL.Query().Get("query"), `"`)

	if q == "" {
		vols := s.ListVolumes()
		members := make([]map[string]any, 0, len(vols))
		for _, v := range vols {
			members = append(members, volumeToMap(v))
		}
		writeJSON(w, http.StatusOK, map[string]any{"total": len(members), "members": members})
		return
	}

	parts := strings.Fields(q)
	if len(parts) != 3 {
		writeJSON(w, http.StatusOK, map[string]any{"total": 0, "members": []any{}})
		return
	}
	field, val := strings.ToLower(parts[0]), parts[2]

	var found *FakeVolume
	switch field {
	case "wwn":
		found, _ = s.FindVolumeByWWN(val)
	case "uuid":
		found, _ = s.FindVolumeByUUID(val)
	}

	if found == nil {
		writeJSON(w, http.StatusOK, map[string]any{"total": 0, "members": []any{}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"total":   1,
		"members": []any{volumeToMap(found)},
	})
}

func handleGetVolume(w http.ResponseWriter, s *State, name string) {
	v, ok := s.GetVolume(name)
	if !ok {
		writeJSON(w, http.StatusNotFound, apiErr("volume not found"))
		return
	}
	writeJSON(w, http.StatusOK, volumeToMap(v))
}

// handleCreateVolume handles POST /api/v1/volumes — creates a new LUN.
func handleCreateVolume(w http.ResponseWriter, r *http.Request, s *State) {
	var body struct {
		Name    string `json:"name"`
		SizeMiB int64  `json:"sizeMiB"`
		// 3PAR also accepts cpg, tpvv, etc. — we ignore them.
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, apiErr(err.Error()))
		return
	}
	if body.Name == "" {
		writeJSON(w, http.StatusBadRequest, apiErr("name required"))
		return
	}
	klog.Infof("storage: CreateVolume name=%s sizeMiB=%d", body.Name, body.SizeMiB)

	v, err := s.CreateVolume(body.Name, body.SizeMiB)
	if err != nil {
		klog.Errorf("storage: CreateVolume %s failed: %v", body.Name, err)
		writeJSON(w, http.StatusConflict, apiErr(err.Error()))
		return
	}
	klog.Infof("storage: created volume %s WWN=%s UUID=%s sizeMiB=%d", v.Name, v.WWN, v.UUID, v.SizeMiB)
	writeJSON(w, http.StatusCreated, volumeToMap(v))
}

// handleVolumeAction handles POST /api/v1/volumes/:name — snapshot or promote.
// Action 1 = create snapshot (3PAR: createPhysicalCopy or createVirtualCopy)
// Action 4 = promote virtual copy (3PAR)
func handleVolumeAction(w http.ResponseWriter, r *http.Request, s *State, srcName string) {
	klog.Infof("storage: VolumeAction src=%s", srcName)
	var body struct {
		Action int    `json:"action"`
		Name   string `json:"name"`   // destination name (for snapshot)
		SnapCPG string `json:"snapCPG"` // ignored
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, apiErr(err.Error()))
		return
	}

	switch body.Action {
	case 1: // createVirtualCopy / snapshot
		snapName := body.Name
		if snapName == "" {
			snapName = srcName + "-snap"
		}
		snap, err := s.CreateSnapshot(srcName, snapName)
		if err != nil {
			writeJSON(w, http.StatusNotFound, apiErr(err.Error()))
			return
		}
		klog.V(2).Infof("storage: snapshot %s → %s", srcName, snapName)
		writeJSON(w, http.StatusCreated, volumeToMap(snap))

	case 4: // promoteVirtualCopy — returns a task
		t := s.StartTask(2000, 4000) // 2-4 seconds
		klog.V(2).Infof("storage: promote %s task %d started", srcName, t.ID)
		writeJSON(w, http.StatusAccepted, map[string]any{"taskid": t.ID})

	default:
		writeJSON(w, http.StatusBadRequest, apiErr(fmt.Sprintf("unknown action %d", body.Action)))
	}
}

// handleRenameVolume handles PUT /api/v1/volumes/:name with newName in body.
func handleRenameVolume(w http.ResponseWriter, r *http.Request, s *State, oldName string) {
	var body struct {
		NewName string `json:"newName"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, apiErr(err.Error()))
		return
	}
	if err := s.RenameVolume(oldName, body.NewName); err != nil {
		writeJSON(w, http.StatusNotFound, apiErr(err.Error()))
		return
	}
	w.WriteHeader(http.StatusOK)
}

func volumeToMap(v *FakeVolume) map[string]any {
	return map[string]any{
		"name":             v.Name,
		"wwn":              v.WWN,
		"uuid":             v.UUID,
		"sizeMiB":          v.SizeMiB,
		"provisioningType": 1,
		"copyOf":          v.ParentName,
		"snapCPG":          "fake-snap-cpg",
	}
}

// --- Hosts ---

func handleCreateHost(w http.ResponseWriter, r *http.Request, s *State) {
	var body struct {
		Name       string `json:"name"`
		ISCSIPaths []struct {
			Name string `json:"name"`
		} `json:"iSCSIPaths"`
		FCPaths []struct {
			WWN string `json:"wwn"`
		} `json:"FCPaths"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, apiErr(err.Error()))
		return
	}
	var iqn string
	if len(body.ISCSIPaths) > 0 {
		iqn = body.ISCSIPaths[0].Name
	}
	name := s.EnsureHost(iqn)
	writeJSON(w, http.StatusCreated, map[string]string{"name": name})
}

func handleListHosts(w http.ResponseWriter, r *http.Request, s *State) {
	q := r.URL.Query().Get("query")
	// Support query like `iscsiPaths[].name EQ <iqn>`
	if strings.Contains(q, " EQ ") {
		parts := strings.SplitN(q, " EQ ", 2)
		iqn := strings.Trim(parts[1], `"`)
		if name, ok := s.GetHostByIQN(iqn); ok {
			writeJSON(w, http.StatusOK, map[string]any{
				"total": 1,
				"members": []any{map[string]string{"name": name}},
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"total": 0, "members": []any{}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"total": 0, "members": []any{}})
}

func handleGetHost(w http.ResponseWriter, s *State, name string) {
	writeJSON(w, http.StatusOK, map[string]string{"name": name})
}

// --- Host sets ---

func handleCreateHostSet(w http.ResponseWriter, r *http.Request, s *State) {
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, apiErr(err.Error()))
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
		writeJSON(w, http.StatusBadRequest, apiErr(err.Error()))
		return
	}
	for _, h := range body.SetMembers {
		s.AddHostToSet(setName, h)
	}
	w.WriteHeader(http.StatusOK)
}

// --- VLUNs ---

func handleCreateVlun(w http.ResponseWriter, r *http.Request, s *State) {
	var body struct {
		VolumeName string `json:"volumeName"`
		Hostname   string `json:"hostname"`
		LunID      int    `json:"lun"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, apiErr(err.Error()))
		return
	}
	klog.Infof("storage: MapLUN volume=%s host=%s", body.VolumeName, body.Hostname)
	lunID := s.MapLUN(body.VolumeName, body.Hostname)
	klog.Infof("storage: VLUN created volume=%s host=%s lunID=%d", body.VolumeName, body.Hostname, lunID)
	writeJSON(w, http.StatusCreated, map[string]any{
		"volumeName": body.VolumeName,
		"hostname":   body.Hostname,
		"lun":        lunID,
	})
}

func handleDeleteVlun(w http.ResponseWriter, r *http.Request, s *State) {
	// /api/v1/vluns/<volumeName>,<lun>,<hostname>
	tail := strings.TrimPrefix(r.URL.Path, "/api/v1/vluns/")
	parts := strings.SplitN(tail, ",", 3)
	if len(parts) < 3 {
		writeJSON(w, http.StatusBadRequest, apiErr("bad vlun path, expected name,lun,host"))
		return
	}
	s.UnmapLUN(parts[0], parts[2])
	w.WriteHeader(http.StatusNoContent)
}

// --- helpers ---

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		klog.V(2).Infof("storage: JSON encode: %v", err)
	}
}

func apiErr(msg string) map[string]string { return map[string]string{"desc": msg} }
