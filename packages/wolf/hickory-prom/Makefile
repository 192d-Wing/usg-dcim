# Build + push the custom Hickory image with `prometheus-metrics`
# enabled. Mirrors the coredns-nsec3sign Makefile in shape so a single
# muscle-memory works across both custom images.

IMAGE           ?= ghcr.io/192d-wing/hickory-prom
HICKORY_VERSION ?= v0.26.0
REVISION        ?= 2
TAG             ?= $(HICKORY_VERSION)-$(REVISION)

# Local-build path for testing in-flight upstream patches before they
# merge (today: the strict-allowlist PR). Builds from a local
# hickory-dns workspace checkout, NOT the pinned upstream tag.
# Override LOCAL_HICKORY_PATH if your checkout lives somewhere else.
LOCAL_TAG          ?= $(HICKORY_VERSION)-strict-dev
LOCAL_HICKORY_PATH ?= ../../../hickory-dns

.PHONY: build push clean local-build local-push

# Single multi-stage build — no separate "test" target because there's
# no project-local Go/Rust source here; the only build artifact is
# the upstream Hickory binary recompiled with our feature flags.
build:
	podman build \
		--build-arg HICKORY_VERSION=$(HICKORY_VERSION) \
		-t $(IMAGE):$(TAG) \
		-t $(IMAGE):latest \
		.

push: build
	podman push $(IMAGE):$(TAG)
	podman push $(IMAGE):latest

# Build the strict-dev image from the local hickory-dns checkout.
# The build context IS that checkout (so `COPY . /src` picks up the
# patched workspace). Tag intentionally distinct from release tags
# so a stale local build can't masquerade as a real release.
local-build:
	podman build \
		-f Containerfile.local \
		-t $(IMAGE):$(LOCAL_TAG) \
		$(LOCAL_HICKORY_PATH)

local-push: local-build
	podman push $(IMAGE):$(LOCAL_TAG)

clean:
	-podman image rm $(IMAGE):$(TAG) $(IMAGE):latest 2>/dev/null
	-podman image rm $(IMAGE):$(LOCAL_TAG) 2>/dev/null
