package csi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// StorageClient is a minimal HTTP client for the DevStorage API server.
// The CSI driver uses it to create/delete volumes so they appear in
// the storage array state — linked to what the xcopy populator sees.
type StorageClient struct {
	baseURL    string
	httpClient *http.Client
}

func NewStorageClient(baseURL string) *StorageClient {
	return &StorageClient{
		baseURL: baseURL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

type storageVolume struct {
	Name    string `json:"name"`
	WWN     string `json:"wwn"`
	UUID    string `json:"uuid"`
	SizeMiB int64  `json:"sizeMiB"`
}

// CreateVolume calls POST /api/v1/volumes and returns the volume name (= VolumeHandle).
func (c *StorageClient) CreateVolume(name string, sizeMiB int64) (storageVolume, error) {
	body, _ := json.Marshal(map[string]any{"name": name, "sizeMiB": sizeMiB})
	resp, err := c.httpClient.Post(c.baseURL+"/api/v1/volumes", "application/json", bytes.NewReader(body))
	if err != nil {
		return storageVolume{}, fmt.Errorf("storage CreateVolume: %w", err)
	}
	defer resp.Body.Close()

	var vol storageVolume
	if resp.StatusCode == http.StatusCreated || resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&vol); err != nil {
			return storageVolume{}, fmt.Errorf("storage CreateVolume decode: %w", err)
		}
		return vol, nil
	}
	var errBody map[string]string
	json.NewDecoder(resp.Body).Decode(&errBody)
	return storageVolume{}, fmt.Errorf("storage CreateVolume: HTTP %d: %s", resp.StatusCode, errBody["desc"])
}

// DeleteVolume calls DELETE /api/v1/volumes/:name.
func (c *StorageClient) DeleteVolume(name string) error {
	req, _ := http.NewRequest(http.MethodDelete, c.baseURL+"/api/v1/volumes/"+name, nil)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("storage DeleteVolume: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("storage DeleteVolume: HTTP %d", resp.StatusCode)
	}
	return nil
}
