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

	// Volumes — CRUD + snapshot/rename/promoteVirtualCopy
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
			// POST /volumes/:name — createSnapshot
			// body: {"action":"createSnapshot","parameters":{"name":"snapName"}}
			handleVolumePost(w, r, state, name)
		case http.MethodPut:
			// PUT /volumes/:name — rename (newName), setSnapCPG (snapCPG), or promoteVirtualCopy (action:4)
			handleVolumePut(w, r, state, name)
		case http.MethodDelete:
			klog.Infof("storage: DeleteVolume %s", name)
			if err := state.DeleteVolume(name); err != nil {
				writeJSON(w, http.StatusInternalServerError, apiErr(err.Error()))
				return
			}
			w.WriteHeader(http.StatusOK)
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
	// GET /hostsets/:name — existence check (EnsureHostSetExists)
	// PUT /hostsets/:name — add members (AddHostToHostSet uses action:1 + setmembers:[...])
	mux.HandleFunc("/api/v1/hostsets/", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/api/v1/hostsets/")
		switch r.Method {
		case http.MethodGet:
			if state.HostSetExists(name) {
				writeJSON(w, http.StatusOK, map[string]string{"name": name})
			} else {
				writeJSON(w, http.StatusNotFound, apiErr("host set not found"))
			}
		case http.MethodPut:
			handleAddHostToSet(w, r, state, name)
		default:
			http.NotFound(w, r)
		}
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

// wsApiBuild2023 is the Primera3Par WSAPI build threshold used in CopyVolume:
// if build >= this value, snapCPG doesn't need to be pre-set on the source volume.
// Return a value >= 2023 so CopyVolume skips the setVolumeSnapCPG call.
const wsApiBuild2023 = 30303040

func handleSystem(w http.ResponseWriter, _ *http.Request, s *State) {
	totalMiB := s.PoolSizeMiB()
	freeMiB := s.FreeMiB()
	writeJSON(w, http.StatusOK, map[string]any{
		"id":                   "DevStorage-001",
		"name":                 "DevStorage Fake Array",
		"model":                "DevArray 9000",
		"serialNumber":         "DEVSTG001",
		"systemVersion":        "4.0.0.0",
		"IPv4Addr":             "127.0.0.1",
		"build":                wsApiBuild2023 + 1, // >= wsApiBuild2023 so CopyVolume skips setSnapCPG
		"totalCapacityMiB":     totalMiB,
		"allocatedCapacityMiB": totalMiB - freeMiB,
		"freeCapacityMiB":      freeMiB,
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

// handleVolumePost handles POST /api/v1/volumes/:name — createSnapshot.
// Primera3Par format: {"action":"createSnapshot","parameters":{"name":"snapName"}}
func handleVolumePost(w http.ResponseWriter, r *http.Request, s *State, srcName string) {
	klog.Infof("storage: VolumePost (snapshot) src=%s", srcName)
	var body struct {
		Action     string         `json:"action"`
		Parameters map[string]any `json:"parameters"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, apiErr(err.Error()))
		return
	}

	if body.Action != "createSnapshot" {
		writeJSON(w, http.StatusBadRequest, apiErr(fmt.Sprintf("unknown action %q", body.Action)))
		return
	}

	snapName, _ := body.Parameters["name"].(string)
	if snapName == "" {
		snapName = srcName + "-snap"
	}
	snap, err := s.CreateSnapshot(srcName, snapName)
	if err != nil {
		writeJSON(w, http.StatusNotFound, apiErr(err.Error()))
		return
	}
	klog.Infof("storage: snapshot created %s → %s", srcName, snapName)
	writeJSON(w, http.StatusCreated, volumeToMap(snap))
}

// handleVolumePut handles PUT /api/v1/volumes/:name.
// Three sub-operations distinguished by body fields:
//   - {"newName": "..."} → rename (renameVolume)
//   - {"snapCPG": "..."} → set snap CPG (ignored, just return OK)
//   - {"action": 4, "online": true} → promoteVirtualCopy (returns taskid)
func handleVolumePut(w http.ResponseWriter, r *http.Request, s *State, name string) {
	klog.Infof("storage: VolumePut name=%s", name)
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, apiErr(err.Error()))
		return
	}

	if newName, ok := body["newName"].(string); ok && newName != "" {
		klog.Infof("storage: rename %s → %s", name, newName)
		if err := s.RenameVolume(name, newName); err != nil {
			writeJSON(w, http.StatusNotFound, apiErr(err.Error()))
			return
		}
		w.WriteHeader(http.StatusOK)
		return
	}

	if _, ok := body["snapCPG"]; ok {
		// setVolumeSnapCPG — our volumes always have snapCPG set, just acknowledge.
		klog.V(2).Infof("storage: setSnapCPG %s (no-op)", name)
		w.WriteHeader(http.StatusOK)
		return
	}

	if action, ok := body["action"].(float64); ok && int(action) == 4 {
		// promoteVirtualCopy
		t := s.StartTask(2000, 4000)
		klog.Infof("storage: promoteVirtualCopy %s task %d started", name, t.ID)
		writeJSON(w, http.StatusAccepted, map[string]any{"taskid": t.ID})
		return
	}

	writeJSON(w, http.StatusBadRequest, apiErr("unrecognised PUT body — expected newName, snapCPG, or action:4"))
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
		ISCSINames []string `json:"iSCSINames"` // 3PAR create-host format
		FCWWNs     []string `json:"FCWWNs"`     // 3PAR FC format
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

	iqns := body.ISCSINames
	for _, p := range body.ISCSIPaths {
		iqns = append(iqns, p.Name)
	}
	wwns := body.FCWWNs
	for _, p := range body.FCPaths {
		wwns = append(wwns, p.WWN)
	}

	var name string
	if len(iqns) > 0 {
		name = s.EnsureHostFull("", iqns, wwns)
	} else if len(wwns) > 0 {
		name = s.EnsureHostFull("", iqns, wwns)
	} else {
		name = body.Name
		if name == "" {
			name = "fake-host-unknown"
		}
		s.EnsureHostFull(name, nil, nil)
	}
	klog.Infof("storage: CreateHost name=%s iqns=%v wwns=%v", name, iqns, wwns)
	writeJSON(w, http.StatusCreated, hostToMap(&FakeHost{Name: name, ISCSIIQNs: iqns, FCWWNs: wwns}))
}

// handleListHosts handles GET /api/v1/hosts and GET /api/v1/hosts?query=...
// Primera3Par query format: `" iSCSIPaths[name EQ <iqn>] "` or `" FCPaths[wwn EQ <wwn>] "`
func handleListHosts(w http.ResponseWriter, r *http.Request, s *State) {
	q := strings.Trim(r.URL.Query().Get("query"), `" `)
	if q != "" {
		// Try IQN filter
		if strings.Contains(q, "iSCSIPaths") && strings.Contains(q, " EQ ") {
			parts := strings.SplitN(q, " EQ ", 2)
			iqn := strings.Trim(parts[1], ` "`)
			if name, ok := s.GetHostByIQN(iqn); ok {
				writeJSON(w, http.StatusOK, map[string]any{"total": 1, "members": []any{map[string]string{"name": name}}})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"total": 0, "members": []any{}})
			return
		}
		// Try FC WWN filter
		if strings.Contains(q, "FCPaths") && strings.Contains(q, " EQ ") {
			parts := strings.SplitN(q, " EQ ", 2)
			wwn := strings.Trim(parts[1], ` "`)
			if name, ok := s.GetHostByFCWWN(wwn); ok {
				writeJSON(w, http.StatusOK, map[string]any{"total": 1, "members": []any{map[string]string{"name": name}}})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"total": 0, "members": []any{}})
			return
		}
	}
	// Return full list with ISCSIPaths/FCPaths for client-side filtering.
	all := s.ListHosts()
	members := make([]any, 0, len(all))
	for _, h := range all {
		members = append(members, hostToMap(h))
	}
	writeJSON(w, http.StatusOK, map[string]any{"total": len(members), "members": members})
}

func handleGetHost(w http.ResponseWriter, s *State, name string) {
	if !s.HostExists(name) {
		writeJSON(w, http.StatusNotFound, apiErr("host not found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"name": name})
}

func hostToMap(h *FakeHost) map[string]any {
	iscsiPaths := make([]map[string]string, 0, len(h.ISCSIIQNs))
	for _, iqn := range h.ISCSIIQNs {
		iscsiPaths = append(iscsiPaths, map[string]string{"name": iqn})
	}
	fcPaths := make([]map[string]string, 0, len(h.FCWWNs))
	for _, wwn := range h.FCWWNs {
		fcPaths = append(fcPaths, map[string]string{"wwn": wwn})
	}
	return map[string]any{
		"name":       h.Name,
		"iSCSIPaths": iscsiPaths,
		"FCPaths":    fcPaths,
	}
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

// handleAddHostToSet handles PUT /api/v1/hostsets/:name
// Primera3Par format: {"action":1,"setmembers":["host1","host2"]}
func handleAddHostToSet(w http.ResponseWriter, r *http.Request, s *State, setName string) {
	var body struct {
		Action     int      `json:"action"`
		SetMembers []string `json:"setmembers"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, apiErr(err.Error()))
		return
	}
	s.EnsureHostSet(setName)
	for _, h := range body.SetMembers {
		klog.Infof("storage: AddHostToSet set=%s host=%s", setName, h)
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
