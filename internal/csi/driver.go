package csi

import (
	"context"
	"fmt"
	"net"
	"os"

	csipb "github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc"
	"k8s.io/klog/v2"
)

const DefaultDriverName = "dev-csi.amitosw15.io"

// TopologyKey is the CSI topology segment key.
const TopologyKey = "dev-csi.amitosw15.io/hostname"

// Mounter abstracts filesystem ops so they can be faked in tests.
type Mounter interface {
	MakeDir(path string) error
	Mount(source, target string, options []string) error
	Unmount(target string) error
	IsMounted(target string) (bool, error)
}

// FailConfig controls per-call failure injection (for tests).
type FailConfig struct {
	FailNextCreate bool
}

// Config holds constructor options.
type Config struct {
	DriverName     string
	Version        string
	NodeName       string
	StagingDir     string
	StorageAPIURL  string // URL of the storage API server (e.g. "http://localhost:8080")
	Mounter        Mounter
}

// Driver implements CSI Identity, Controller, and Node in a single process.
type Driver struct {
	name          string
	version       string
	nodeName      string
	stagingDir    string
	state         *State          // tracks mount state only
	storageClient *StorageClient  // delegates volume CRUD to the API server
	mounter       Mounter
	Fail          FailConfig

	csipb.UnimplementedIdentityServer
	csipb.UnimplementedControllerServer
	csipb.UnimplementedNodeServer
}

func New(cfg Config) *Driver {
	name := cfg.DriverName
	if name == "" {
		name = DefaultDriverName
	}
	version := cfg.Version
	if version == "" {
		version = "v0.0.1"
	}
	stagingDir := cfg.StagingDir
	if stagingDir == "" {
		stagingDir = "/var/lib/dev-csi/volumes"
	}
	m := cfg.Mounter
	if m == nil {
		m = &osMounter{}
	}
	var sc *StorageClient
	if cfg.StorageAPIURL != "" {
		sc = NewStorageClient(cfg.StorageAPIURL)
	}
	return &Driver{
		name:          name,
		version:       version,
		nodeName:      cfg.NodeName,
		stagingDir:    stagingDir,
		state:         NewState(),
		storageClient: sc,
		mounter:       m,
	}
}

// Serve starts the gRPC server on socketPath and blocks until ctx is cancelled.
// ready is closed once the listener is accepting connections.
func (d *Driver) Serve(ctx context.Context, socketPath string, ready chan<- struct{}) error {
	if err := os.Remove(socketPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove existing socket: %w", err)
	}
	lis, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("listen %s: %w", socketPath, err)
	}

	srv := grpc.NewServer(grpc.UnaryInterceptor(logInterceptor))
	csipb.RegisterIdentityServer(srv, d)
	csipb.RegisterControllerServer(srv, d)
	csipb.RegisterNodeServer(srv, d)

	if ready != nil {
		close(ready)
	}
	klog.Infof("dev-csi driver %s serving on %s", d.name, socketPath)

	go func() {
		<-ctx.Done()
		srv.GracefulStop()
	}()
	return srv.Serve(lis)
}

func logInterceptor(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	klog.V(4).Infof("CSI call: %s", info.FullMethod)
	resp, err := handler(ctx, req)
	if err != nil {
		klog.V(2).Infof("CSI error %s: %v", info.FullMethod, err)
	}
	return resp, err
}
