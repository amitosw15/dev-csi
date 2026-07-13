package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/amitosw15/dev-csi/internal/csi"
	"github.com/amitosw15/dev-csi/internal/storage"
	"k8s.io/klog/v2"
)

func main() {
	var (
		driverName  = flag.String("driver-name", csi.DefaultDriverName, "CSI driver name")
		version     = flag.String("version", "v0.1.0", "Driver version")
		socketPath  = flag.String("socket-path", "/csi/csi.sock", "Unix socket path for CSI gRPC")
		stagingDir  = flag.String("staging-dir", "/var/lib/dev-csi/volumes", "Per-volume staging dir")
		httpAddr    = flag.String("http-addr", ":8080", "Address for HTTP storage API server")
		volumesFile    = flag.String("volumes-file", "", "JSON file with seed volumes (see FakeVolume)")
		poolSizeTiB   = flag.Int64("pool-size-tib", 10, "Storage pool size in TiB")
		storageAPIURL = flag.String("storage-api-url", "http://localhost:8080", "URL of storage API server (used by CSI driver)")
	)
	klog.InitFlags(nil)
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()

	// Load seed volumes from file if provided.
	var seed []storage.FakeVolume
	if *volumesFile != "" {
		data, err := os.ReadFile(*volumesFile)
		if err != nil {
			klog.Fatalf("read volumes file: %v", err)
		}
		if err := json.Unmarshal(data, &seed); err != nil {
			klog.Fatalf("parse volumes file: %v", err)
		}
		klog.Infof("seeded %d fake volumes from %s", len(seed), *volumesFile)
	}

	storageState := storage.NewState(*poolSizeTiB*1024*1024, seed)

	// Start HTTP storage API server.
	httpSrv := &http.Server{
		Addr:    *httpAddr,
		Handler: storage.NewServer(storageState),
	}
	go func() {
		klog.Infof("storage API listening on %s", *httpAddr)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			klog.Errorf("HTTP server: %v", err)
		}
	}()
	go func() {
		<-ctx.Done()
		_ = httpSrv.Shutdown(context.Background())
	}()

	// Start CSI gRPC driver.
	d := csi.New(csi.Config{
		DriverName:    *driverName,
		Version:       *version,
		NodeName:      os.Getenv("NODE_NAME"),
		StagingDir:    *stagingDir,
		StorageAPIURL: *storageAPIURL,
	})
	if err := d.Serve(ctx, *socketPath, nil); err != nil && ctx.Err() == nil {
		klog.Fatalf("CSI driver: %v", err)
	}
	fmt.Println("dev-csi stopped")
}
