package storage_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/amitosw15/dev-csi/internal/storage"
)

func newTestServer(seed ...storage.FakeVolume) (*httptest.Server, *storage.State) {
	state := storage.NewState(seed)
	srv := httptest.NewServer(storage.NewServer(state))
	return srv, state
}

func TestSessionCreateAndDelete(t *testing.T) {
	srv, _ := newTestServer()
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/v1/credentials", "application/json",
		bytes.NewBufferString(`{"user":"admin","password":"secret"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}
	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	key := body["key"]
	if key == "" {
		t.Fatal("expected non-empty session key")
	}

	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/v1/credentials/"+key, nil)
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp2.StatusCode)
	}
}

func TestListVolumesByWWN(t *testing.T) {
	vol := storage.FakeVolume{
		Name: "test-vol-001",
		WWN:  "60002AC000000000000000010000B5D6",
		UUID: "550e8400-e29b-41d4-a716-446655440000",
		Size: 10 << 30,
	}
	srv, _ := newTestServer(vol)
	defer srv.Close()

	// Query by WWN
	resp, err := http.Get(srv.URL + "/api/v1/volumes?query=%22wwn+EQ+60002AC000000000000000010000B5D6%22")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if total := result["total"].(float64); total != 1 {
		t.Errorf("expected 1 result, got %v", total)
	}
	members := result["members"].([]any)
	name := members[0].(map[string]any)["name"].(string)
	if name != "test-vol-001" {
		t.Errorf("expected test-vol-001, got %s", name)
	}
}

func TestListVolumesByUUID(t *testing.T) {
	vol := storage.FakeVolume{
		Name: "test-vol-002",
		WWN:  "AABBCC001122334455667788",
		UUID: "deadbeef-dead-dead-dead-deadbeefcafe",
		Size: 5 << 30,
	}
	srv, _ := newTestServer(vol)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/volumes?query=%22uuid+EQ+deadbeef-dead-dead-dead-deadbeefcafe%22")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var result map[string]any
	json.NewDecoder(resp.Body).Decode(&result)
	if total := result["total"].(float64); total != 1 {
		t.Errorf("expected 1, got %v", total)
	}
}

func TestVolumeNotFound(t *testing.T) {
	srv, _ := newTestServer()
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/volumes?query=%22wwn+EQ+NOTEXIST%22")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var result map[string]any
	json.NewDecoder(resp.Body).Decode(&result)
	if total := result["total"].(float64); total != 0 {
		t.Errorf("expected 0, got %v", total)
	}
}

func TestXcopyMapUnmapFlow(t *testing.T) {
	vol := storage.FakeVolume{Name: "xcopy-vol", WWN: "DEADBEEF00000001", Size: 1 << 30}
	srv, _ := newTestServer(vol)
	defer srv.Close()

	// Create host set
	resp, _ := http.Post(srv.URL+"/api/v1/hostsets", "application/json",
		bytes.NewBufferString(`{"name":"clonner-group-1"}`))
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create hostset: got %d", resp.StatusCode)
	}

	// Create VLUN (map)
	resp, _ = http.Post(srv.URL+"/api/v1/vluns", "application/json",
		bytes.NewBufferString(`{"volumeName":"xcopy-vol","hostname":"clonner-group-1"}`))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("map vlun: got %d", resp.StatusCode)
	}

	// List VLUNs
	resp2, _ := http.Get(srv.URL + "/api/v1/vluns")
	defer resp2.Body.Close()
	var list map[string]any
	json.NewDecoder(resp2.Body).Decode(&list)
	if total := list["total"].(float64); total != 1 {
		t.Errorf("expected 1 vlun, got %v", total)
	}

	// Unmap
	req, _ := http.NewRequest(http.MethodDelete,
		srv.URL+"/api/v1/vluns/xcopy-vol,1,clonner-group-1", nil)
	resp3, _ := http.DefaultClient.Do(req)
	resp3.Body.Close()
	if resp3.StatusCode != http.StatusNoContent {
		t.Fatalf("unmap: got %d", resp3.StatusCode)
	}
}
