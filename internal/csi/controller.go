package csi

import (
	"context"
	"fmt"

	csipb "github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog/v2"
)

func (d *Driver) CreateVolume(_ context.Context, req *csipb.CreateVolumeRequest) (*csipb.CreateVolumeResponse, error) {
	name := req.GetName()
	klog.Infof("CreateVolume: name=%s hasStorageAPI=%v", name, d.storageClient != nil)

	if d.Fail.FailNextCreate {
		d.Fail.FailNextCreate = false
		klog.Warningf("CreateVolume: injected failure for %s", name)
		return nil, status.Error(codes.Internal, "injected failure: CreateVolume")
	}
	if name == "" {
		return nil, status.Error(codes.InvalidArgument, "volume name required")
	}

	var capBytes int64
	if cr := req.GetCapacityRange(); cr != nil {
		capBytes = cr.GetRequiredBytes()
	}
	if capBytes == 0 {
		capBytes = 1 << 30
	}
	sizeMiB := capBytes / (1 << 20)
	if sizeMiB == 0 {
		sizeMiB = 1
	}
	klog.V(2).Infof("CreateVolume: %s requesting %d MiB", name, sizeMiB)

	if d.storageClient != nil {
		// Check local cache first (idempotent).
		if existing, ok := d.state.GetByName(name); ok {
			klog.Infof("CreateVolume: idempotent hit in local cache for %s (id=%s)", name, existing.ID)
			return d.buildCreateResponse(existing.ID, existing.CapacityBytes, req), nil
		}
		storVol, err := d.storageClient.CreateVolume(name, sizeMiB)
		if err != nil {
			klog.Errorf("CreateVolume: storage API error for %s: %v", name, err)
			return nil, status.Errorf(codes.Internal, "storage API: %v", err)
		}
		d.state.Add(Volume{ID: storVol.Name, Name: name, CapacityBytes: capBytes})
		klog.Infof("CreateVolume: created %s WWN=%s size=%dMiB", storVol.Name, storVol.WWN, sizeMiB)
		return d.buildCreateResponse(storVol.Name, capBytes, req), nil
	}

	// Fallback: fully in-memory.
	if existing, ok := d.state.GetByName(name); ok {
		klog.Infof("CreateVolume: idempotent (in-memory) for %s", name)
		return d.buildCreateResponse(existing.ID, existing.CapacityBytes, req), nil
	}
	id := fmt.Sprintf("dev-csi-vol-%s", name)
	d.state.Add(Volume{ID: id, Name: name, CapacityBytes: capBytes})
	klog.Infof("CreateVolume: created in-memory %s", id)
	return d.buildCreateResponse(id, capBytes, req), nil
}

func (d *Driver) buildCreateResponse(id string, capBytes int64, req *csipb.CreateVolumeRequest) *csipb.CreateVolumeResponse {
	vol := &csipb.Volume{VolumeId: id, CapacityBytes: capBytes}
	if req.GetAccessibilityRequirements() != nil {
		if p := req.GetAccessibilityRequirements().GetPreferred(); len(p) > 0 {
			vol.AccessibleTopology = []*csipb.Topology{p[0]}
			klog.V(2).Infof("CreateVolume: topology=%v", p[0].GetSegments())
		}
	}
	return &csipb.CreateVolumeResponse{Volume: vol}
}

func (d *Driver) DeleteVolume(_ context.Context, req *csipb.DeleteVolumeRequest) (*csipb.DeleteVolumeResponse, error) {
	id := req.GetVolumeId()
	klog.Infof("DeleteVolume: id=%s", id)
	if id == "" {
		return nil, status.Error(codes.InvalidArgument, "volume ID required")
	}
	if d.storageClient != nil {
		if err := d.storageClient.DeleteVolume(id); err != nil {
			klog.Errorf("DeleteVolume: storage API error for %s: %v", id, err)
			return nil, status.Errorf(codes.Internal, "storage API: %v", err)
		}
		klog.Infof("DeleteVolume: deleted %s from storage API", id)
	}
	d.state.Delete(id)
	return &csipb.DeleteVolumeResponse{}, nil
}

func (d *Driver) ListVolumes(_ context.Context, _ *csipb.ListVolumesRequest) (*csipb.ListVolumesResponse, error) {
	vols := d.state.List()
	klog.V(2).Infof("ListVolumes: returning %d local entries", len(vols))
	entries := make([]*csipb.ListVolumesResponse_Entry, 0, len(vols))
	for _, v := range vols {
		entries = append(entries, &csipb.ListVolumesResponse_Entry{
			Volume: &csipb.Volume{VolumeId: v.ID, CapacityBytes: v.CapacityBytes},
		})
	}
	return &csipb.ListVolumesResponse{Entries: entries}, nil
}

func (d *Driver) ControllerGetCapabilities(_ context.Context, _ *csipb.ControllerGetCapabilitiesRequest) (*csipb.ControllerGetCapabilitiesResponse, error) {
	klog.V(4).Info("ControllerGetCapabilities")
	caps := []csipb.ControllerServiceCapability_RPC_Type{
		csipb.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME,
		csipb.ControllerServiceCapability_RPC_LIST_VOLUMES,
	}
	out := make([]*csipb.ControllerServiceCapability, 0, len(caps))
	for _, c := range caps {
		out = append(out, &csipb.ControllerServiceCapability{
			Type: &csipb.ControllerServiceCapability_Rpc{
				Rpc: &csipb.ControllerServiceCapability_RPC{Type: c},
			},
		})
	}
	return &csipb.ControllerGetCapabilitiesResponse{Capabilities: out}, nil
}
