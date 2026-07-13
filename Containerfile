FROM registry.access.redhat.com/ubi9/go-toolset:1.25.3-1766449309 AS builder
USER 0
WORKDIR /app
COPY . .
ENV GOCACHE=/go-build/cache
RUN --mount=type=cache,target=${GOCACHE},uid=1001 \
    go build -buildvcs=false -ldflags="-w -s" -o dev-csi ./cmd/dev-csi/

FROM registry.access.redhat.com/ubi9-minimal:9.6-1752587672
RUN microdnf install -y util-linux && microdnf clean all
COPY --from=builder /app/dev-csi /usr/local/bin/dev-csi
ENTRYPOINT ["/usr/local/bin/dev-csi"]
LABEL \
    name="dev-csi" \
    description="Fake CSI driver + HPE Primera storage API for e2e testing xcopy and CSI import flows" \
    license="Apache License 2.0"
