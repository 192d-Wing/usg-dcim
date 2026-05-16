# Build + test targets for the custom CoreDNS image that includes
# the nsec3sign plugin. Everything here is plain `podman` and `go`
# so the same commands work in CI and on a developer laptop.

IMAGE           ?= ghcr.io/192d-wing/coredns-nsec3sign
COREDNS_VERSION ?= v1.14.2
REVISION        ?= 1
TAG             ?= $(COREDNS_VERSION)-$(REVISION)

.PHONY: build push test fmt vet clean

# Produce the multi-stage image. The COREDNS_VERSION arg is forwarded
# so a single make invocation can re-pin against a newer CoreDNS
# release without editing the Containerfile.
build:
	podman build \
		--build-arg COREDNS_VERSION=$(COREDNS_VERSION) \
		-t $(IMAGE):$(TAG) \
		-t $(IMAGE):latest \
		.

push: build
	podman push $(IMAGE):$(TAG)
	podman push $(IMAGE):latest

# Unit tests run against the Go module directly — no Docker needed.
# Useful in the inner loop while iterating on parse() and the
# (forthcoming) chain / signer code.
test:
	cd nsec3sign && go test ./... -race -count=1

fmt:
	cd nsec3sign && gofmt -s -w .

vet:
	cd nsec3sign && go vet ./...

clean:
	-podman image rm $(IMAGE):$(TAG) $(IMAGE):latest 2>/dev/null
