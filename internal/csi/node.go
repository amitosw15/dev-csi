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
	klog.Infof("NodePublishVolume: volID=%s target=%s", volID, target)

	if volID == "" {
		return nil, status.Error(codes.InvalidArgument, "volume ID required")
	}
	if target == "" {
		return nil, status.Error(codes.InvalidArgument, "target path required")
	}

	if _, ok := d.state.Get(volID); !ok {
		// Volume may have been created on the provisioner leader (different pod).
		// Fetch from the storage API and cache locally so NodePublishVolume can proceed.
		if d.storageClient != nil {
			klog.V(2).Infof("NodePublishVolume: %s not in local cache, fetching from storage API", volID)
			if err := d.storageClient.EnsureLocal(d.state, volID); err != nil {
				klog.Errorf("NodePublishVolume: volume %s not found on storage API: %v", volID, err)
				return nil, status.Errorf(codes.NotFound, "volume %q not found: %v", volID, err)
			}
			klog.Infof("NodePublishVolume: %s fetched from storage API and cached locally", volID)
		} else {
			klog.Errorf("NodePublishVolume: volume %s not found in local state", volID)
			return nil, status.Errorf(codes.NotFound, "volume %q not found", volID)
		}
	}

	if d.state.IsMounted(volID, target) {
		klog.V(2).Infof("NodePublishVolume: %s already mounted at %s (idempotent)", volID, target)
		return &csipb.NodePublishVolumeResponse{}, nil
	}

	source := filepath.Join(d.stagingDir, volID)
	klog.V(2).Infof("NodePublishVolume: creating staging dir %s", source)
	if err := d.mounter.MakeDir(source); err != nil {
		klog.Errorf("NodePublishVolume: failed to create staging dir %s: %v", source, err)
		return nil, status.Errorf(codes.Internal, "create staging dir: %v", err)
	}
	if err := d.mounter.MakeDir(target); err != nil && !os.IsExist(err) {
		klog.Errorf("NodePublishVolume: failed to create target dir %s: %v", target, err)
		return nil, status.Errorf(codes.Internal, "create target dir: %v", err)
	}
	if err := d.mounter.Mount(source, target, []string{"bind"}); err != nil {
		klog.Errorf("NodePublishVolume: bind mount %s → %s failed: %v", source, target, err)
		return nil, status.Errorf(codes.Internal, "mount: %v", err)
	}

	d.state.AddMount(volID, target)
	klog.Infof("NodePublishVolume: successfully mounted %s → %s", volID, target)
	return &csipb.NodePublishVolumeResponse{}, nil
}

func (d *Driver) NodeUnpublishVolume(_ context.Context, req *csipb.NodeUnpublishVolumeRequest) (*csipb.NodeUnpublishVolumeResponse, error) {
	volID := req.GetVolumeId()
	target := req.GetTargetPath()
	klog.Infof("NodeUnpublishVolume: volID=%s target=%s", volID, target)

	if target == "" {
		return nil, status.Error(codes.InvalidArgument, "target path required")
	}
	mounted, err := d.mounter.IsMounted(target)
	if err != nil {
		klog.Errorf("NodeUnpublishVolume: IsMounted(%s) error: %v", target, err)
		return nil, status.Errorf(codes.Internal, "check mount: %v", err)
	}
	if !mounted {
		klog.V(2).Infof("NodeUnpublishVolume: %s not mounted (idempotent)", target)
		return &csipb.NodeUnpublishVolumeResponse{}, nil
	}
	if err := d.mounter.Unmount(target); err != nil {
		klog.Errorf("NodeUnpublishVolume: unmount %s failed: %v", target, err)
		return nil, status.Errorf(codes.Internal, "unmount: %v", err)
	}
	d.state.RemoveMount(volID, target)
	klog.Infof("NodeUnpublishVolume: unmounted %s from %s", volID, target)
	return &csipb.NodeUnpublishVolumeResponse{}, nil
}

func (d *Driver) NodeGetCapabilities(_ context.Context, _ *csipb.NodeGetCapabilitiesRequest) (*csipb.NodeGetCapabilitiesResponse, error) {
	klog.V(4).Info("NodeGetCapabilities")
	return &csipb.NodeGetCapabilitiesResponse{}, nil
}

func (d *Driver) NodeGetInfo(_ context.Context, _ *csipb.NodeGetInfoRequest) (*csipb.NodeGetInfoResponse, error) {
	resp := &csipb.NodeGetInfoResponse{NodeId: d.nodeName}
	if d.nodeName != "" {
		resp.AccessibleTopology = &csipb.Topology{
			Segments: map[string]string{TopologyKey: d.nodeName},
		}
	}
	klog.Infof("NodeGetInfo: nodeId=%s topology=%v", d.nodeName, resp.AccessibleTopology.GetSegments())
	return resp, nil
}
