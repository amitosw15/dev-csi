# DevStorage

A fake storage array + CSI driver for testing [Forklift](https://github.com/kubev2v/forklift) xcopy and CSI import flows **without real storage hardware**.

> **Internal Red Hat tooling.** See [LICENSE](LICENSE) for usage restrictions.

---

## What it is

Two servers in one binary, deployed on Kubernetes:

```
dev-csi-api  (Deployment ×1)          dev-csi (DaemonSet ×5, per node)
─────────────────────────────          ─────────────────────────────────
HTTP storage API :8080                 CSI gRPC /csi/csi.sock
Mimics HPE Primera/3PAR WSAPI         node-driver-registrar
Single shared in-memory state          external-provisioner
  └ volumes (LUNs) + WWN/UUID           └ CreateVolume → POST dev-csi-api:8080
  └ hosts + host sets                   └ NodePublishVolume (bind-mount)
  └ VLUNs (iSCSI mappings)
  └ tasks (async xcopy, 2-4s delay)
  └ pool capacity tracking (10 TiB)
```

### Everything is linked

When a PVC is created:
1. `external-provisioner` calls CSI `CreateVolume`
2. CSI driver calls `POST /api/v1/volumes` → LUN created with **deterministic WWN** (SHA256 of name)
3. PV `volumeHandle` = LUN name → xcopy populator and HpeImporter can look it up

## Install

### Option A — Raw YAML

```bash
kubectl apply -f https://raw.githubusercontent.com/amitosw15/dev-csi/main/deploy/dev-csi.yaml
```

### Option B — Helm

```bash
helm install dev-csi oci://ghcr.io/amitosw15/dev-csi/charts/dev-csi \
  --namespace openshift-mtv \
  --create-namespace
```

Or from source:

```bash
helm install dev-csi ./charts/dev-csi/ -n openshift-mtv
```

### Customize via Helm values

```yaml
# values.yaml
poolSizeTiB: 20
seedVolumes:
  - Name: "source-vol-001"
    WWN: "60002AC000000000000000010000B5D6"
    UUID: "550e8400-e29b-41d4-a716-446655440000"
    SizeMiB: 102400
```

## Use with Forklift xcopy populator

No Forklift code changes needed. Just set:

```bash
STORAGE_HOSTNAME=http://dev-csi-api.openshift-mtv.svc:8080
STORAGE_VENDOR=primera3par
```

The existing primera3par adapter in `vsphere-copy-offload-populator` speaks the same WSAPI that DevStorage serves.

## Use with Forklift CSI import (HpeImporter)

Same `STORAGE_HOSTNAME`. The HpeImporter resolves volumes by WWN:

```
GET /api/v1/volumes?query="wwn EQ <wwn>"
```

DevStorage returns the volume name → HpeImporter writes `csi.hpe.com/importVolAsClone: <name>` on the PVC.

## API surface (Primera/3PAR WSAPI compatible)

| Method | Endpoint | Used by |
|--------|----------|---------|
| POST | `/api/v1/credentials` | All (session auth) |
| DELETE | `/api/v1/credentials/:key` | All (session teardown) |
| GET | `/api/v1/system` | xcopy (metrics, build check) |
| POST | `/api/v1/volumes` | CSI CreateVolume |
| GET | `/api/v1/volumes` | CSI import (WWN/UUID lookup) |
| GET | `/api/v1/volumes/:name` | xcopy (GetLunDetailsByVolumeName) |
| POST | `/api/v1/volumes/:name` | xcopy (createSnapshot) |
| PUT | `/api/v1/volumes/:name` | xcopy (rename, setSnapCPG, promoteVirtualCopy) |
| DELETE | `/api/v1/volumes/:name` | CSI DeleteVolume |
| GET/POST | `/api/v1/hosts` | xcopy (host management) |
| GET | `/api/v1/hosts/:name` | xcopy (hostExists check) |
| GET/POST | `/api/v1/hostsets` | xcopy (initiator group) |
| GET/PUT | `/api/v1/hostsets/:name` | xcopy (membership) |
| GET/POST | `/api/v1/vluns` | xcopy (map/unmap LUNs) |
| DELETE | `/api/v1/vluns/:vol,:lun,:host` | xcopy (unmap) |
| GET | `/api/v1/tasks/:id` | xcopy (async task polling, 2-4s delay) |

## Development

```bash
make build    # go build ./...
make test     # go test ./...
make image    # podman build
make push     # podman push
make deploy   # kubectl apply -f deploy/dev-csi.yaml
```

After code changes:

```bash
make image push
kubectl rollout restart daemonset/dev-csi deployment/dev-csi-api -n openshift-mtv
```

## StorageClasses

| Name | Binding mode |
|------|-------------|
| `dev-csi-immediate` | Immediate — PVC binds right away |
| `dev-csi-wffc` | WaitForFirstConsumer — PVC binds only after a pod is scheduled |

## Architecture decisions

- **Shared state via API Deployment**: one `dev-csi-api` Deployment holds all in-memory LUN state. All 5 DaemonSet pods call it via `http://dev-csi-api.svc:8080` so volumes created on the provisioner-leader node are visible everywhere.
- **Deterministic WWN**: `SHA256("wwn:" + volumeName)` → always the same WWN for the same volume name, so tests can assert exact values.
- **Auto-recreate on restart**: if the API server restarts and loses state, `NodePublishVolume` auto-recreates the LUN entry so existing PVCs still mount.
- **Task simulation**: xcopy `promoteVirtualCopy` returns a task ID that transitions ACTIVE→DONE after a random 2–4 second delay.
