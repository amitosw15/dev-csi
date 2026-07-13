IMAGE ?= quay.io/amitosw15/dev-csi:devel
NAMESPACE ?= openshift-mtv

.PHONY: build test image push deploy undeploy

build:
	go build ./...

test:
	go test ./... -v -count=1

image:
	podman build -t $(IMAGE) -f Containerfile .

push: image
	podman push $(IMAGE)

deploy:
	kubectl apply -f deploy/dev-csi.yaml

undeploy:
	kubectl delete -f deploy/dev-csi.yaml --ignore-not-found
