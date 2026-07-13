package csi

import (
	"context"

	csipb "github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func (d *Driver) GetPluginInfo(_ context.Context, _ *csipb.GetPluginInfoRequest) (*csipb.GetPluginInfoResponse, error) {
	return &csipb.GetPluginInfoResponse{Name: d.name, VendorVersion: d.version}, nil
}

func (d *Driver) GetPluginCapabilities(_ context.Context, _ *csipb.GetPluginCapabilitiesRequest) (*csipb.GetPluginCapabilitiesResponse, error) {
	return &csipb.GetPluginCapabilitiesResponse{
		Capabilities: []*csipb.PluginCapability{
			{Type: &csipb.PluginCapability_Service_{
				Service: &csipb.PluginCapability_Service{Type: csipb.PluginCapability_Service_CONTROLLER_SERVICE},
			}},
		},
	}, nil
}

func (d *Driver) Probe(_ context.Context, _ *csipb.ProbeRequest) (*csipb.ProbeResponse, error) {
	return &csipb.ProbeResponse{Ready: wrapperspb.Bool(true)}, nil
}
