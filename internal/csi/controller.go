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
	if d.Fail.FailNextCreate {
		d.Fail.FailNextCreate = false
		return nil, status.Error(codes.Internal, "injected failure: CreateVolume")
	}

	name := req.GetName()
	if name == "" {
		return nil, status.Error(codes.InvalidArgument, "volume name required")
	}

	if existing, ok := d.state.GetByName(name); ok {
		klog.V(4).Infof("CreateVolume idempotent: %s", name)
		vol := &csipb.Volume{VolumeId: existing.ID, CapacityBytes: existing.CapacityBytes}
		if req.GetAccessibilityRequirements() != nil {
			if p := req.GetAccessibilityRequirements().GetPreferred(); len(p) > 0 {
				vol.AccessibleTopology = []*csipb.Topology{p[0]}
			}
		}
		return &csipb.CreateVolumeResponse{Volume: vol}, nil
	}

	var cap int64
	if cr := req.GetCapacityRange(); cr != nil {
		cap = cr.GetRequiredBytes()
	}
	if cap == 0 {
		cap = 1 << 30
	}

	id := fmt.Sprintf("dev-csi-vol-%s", name)
	d.state.Add(Volume{ID: id, Name: name, CapacityBytes: cap})
	klog.V(2).Infof("CreateVolume: %s (%d bytes)", id, cap)

	vol := &csipb.Volume{VolumeId: id, CapacityBytes: cap}
	if req.GetAccessibilityRequirements() != nil {
		if p := req.GetAccessibilityRequirements().GetPreferred(); len(p) > 0 {
			vol.AccessibleTopology = []*csipb.Topology{p[0]}
		}
	}
	return &csipb.CreateVolumeResponse{Volume: vol}, nil
}

func (d *Driver) DeleteVolume(_ context.Context, req *csipb.DeleteVolumeRequest) (*csipb.DeleteVolumeResponse, error) {
	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "volume ID required")
	}
	d.state.Delete(req.GetVolumeId())
	return &csipb.DeleteVolumeResponse{}, nil
}

func (d *Driver) ListVolumes(_ context.Context, _ *csipb.ListVolumesRequest) (*csipb.ListVolumesResponse, error) {
	vols := d.state.List()
	entries := make([]*csipb.ListVolumesResponse_Entry, 0, len(vols))
	for _, v := range vols {
		entries = append(entries, &csipb.ListVolumesResponse_Entry{
			Volume: &csipb.Volume{VolumeId: v.ID, CapacityBytes: v.CapacityBytes},
		})
	}
	return &csipb.ListVolumesResponse{Entries: entries}, nil
}

func (d *Driver) ControllerGetCapabilities(_ context.Context, _ *csipb.ControllerGetCapabilitiesRequest) (*csipb.ControllerGetCapabilitiesResponse, error) {
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
