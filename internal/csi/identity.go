package csi

import (
	"context"

	csipb "github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/protobuf/types/known/wrapperspb"
	"k8s.io/klog/v2"
)

func (d *Driver) GetPluginInfo(_ context.Context, _ *csipb.GetPluginInfoRequest) (*csipb.GetPluginInfoResponse, error) {
	klog.V(4).Infof("GetPluginInfo: name=%s version=%s", d.name, d.version)
	return &csipb.GetPluginInfoResponse{Name: d.name, VendorVersion: d.version}, nil
}

func (d *Driver) GetPluginCapabilities(_ context.Context, _ *csipb.GetPluginCapabilitiesRequest) (*csipb.GetPluginCapabilitiesResponse, error) {
	klog.V(4).Info("GetPluginCapabilities: advertising CONTROLLER_SERVICE")
	return &csipb.GetPluginCapabilitiesResponse{
		Capabilities: []*csipb.PluginCapability{
			{Type: &csipb.PluginCapability_Service_{
				Service: &csipb.PluginCapability_Service{Type: csipb.PluginCapability_Service_CONTROLLER_SERVICE},
			}},
		},
	}, nil
}

func (d *Driver) Probe(_ context.Context, _ *csipb.ProbeRequest) (*csipb.ProbeResponse, error) {
	klog.V(4).Info("Probe: ready=true")
	return &csipb.ProbeResponse{Ready: wrapperspb.Bool(true)}, nil
}
