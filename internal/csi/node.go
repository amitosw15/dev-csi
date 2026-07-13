package csi

import (
	"context"
	"os"
	"path/filepath"

	csipb "github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog/v2"
)

func (d *Driver) NodePublishVolume(_ context.Context, req *csipb.NodePublishVolumeRequest) (*csipb.NodePublishVolumeResponse, error) {
	volID := req.GetVolumeId()
	target := req.GetTargetPath()
	if volID == "" {
		return nil, status.Error(codes.InvalidArgument, "volume ID required")
	}
	if target == "" {
		return nil, status.Error(codes.InvalidArgument, "target path required")
	}
	if _, ok := d.state.Get(volID); !ok {
		return nil, status.Errorf(codes.NotFound, "volume %q not found", volID)
	}
	if d.state.IsMounted(volID, target) {
		return &csipb.NodePublishVolumeResponse{}, nil
	}

	source := filepath.Join(d.stagingDir, volID)
	if err := d.mounter.MakeDir(source); err != nil {
		return nil, status.Errorf(codes.Internal, "create staging dir: %v", err)
	}
	if err := d.mounter.MakeDir(target); err != nil && !os.IsExist(err) {
		return nil, status.Errorf(codes.Internal, "create target dir: %v", err)
	}
	if err := d.mounter.Mount(source, target, []string{"bind"}); err != nil {
		return nil, status.Errorf(codes.Internal, "mount: %v", err)
	}
	d.state.AddMount(volID, target)
	klog.V(2).Infof("NodePublishVolume: %s → %s", volID, target)
	return &csipb.NodePublishVolumeResponse{}, nil
}

func (d *Driver) NodeUnpublishVolume(_ context.Context, req *csipb.NodeUnpublishVolumeRequest) (*csipb.NodeUnpublishVolumeResponse, error) {
	target := req.GetTargetPath()
	if target == "" {
		return nil, status.Error(codes.InvalidArgument, "target path required")
	}
	mounted, err := d.mounter.IsMounted(target)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "check mount: %v", err)
	}
	if !mounted {
		return &csipb.NodeUnpublishVolumeResponse{}, nil
	}
	if err := d.mounter.Unmount(target); err != nil {
		return nil, status.Errorf(codes.Internal, "unmount: %v", err)
	}
	d.state.RemoveMount(req.GetVolumeId(), target)
	return &csipb.NodeUnpublishVolumeResponse{}, nil
}

func (d *Driver) NodeGetCapabilities(_ context.Context, _ *csipb.NodeGetCapabilitiesRequest) (*csipb.NodeGetCapabilitiesResponse, error) {
	return &csipb.NodeGetCapabilitiesResponse{}, nil
}

func (d *Driver) NodeGetInfo(_ context.Context, _ *csipb.NodeGetInfoRequest) (*csipb.NodeGetInfoResponse, error) {
	resp := &csipb.NodeGetInfoResponse{NodeId: d.nodeName}
	if d.nodeName != "" {
		resp.AccessibleTopology = &csipb.Topology{
			Segments: map[string]string{TopologyKey: d.nodeName},
		}
	}
	return resp, nil
}
