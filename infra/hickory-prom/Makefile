# Build + push the custom Hickory image with `prometheus-metrics`
# enabled. Mirrors the coredns-nsec3sign Makefile in shape so a single
# muscle-memory works across both custom images.

IMAGE           ?= ghcr.io/192d-wing/hickory-prom
HICKORY_VERSION ?= v0.26.0
REVISION        ?= 1
TAG             ?= $(HICKORY_VERSION)-$(REVISION)

.PHONY: build push clean

# Single multi-stage build — no separate "test" target because there's
# no project-local Go/Rust source here; the only build artifact is
# the upstream Hickory binary recompiled with our feature flags.
build:
	docker build \
		--build-arg HICKORY_VERSION=$(HICKORY_VERSION) \
		-t $(IMAGE):$(TAG) \
		-t $(IMAGE):latest \
		.

push: build
	docker push $(IMAGE):$(TAG)
	docker push $(IMAGE):latest

clean:
	-docker image rm $(IMAGE):$(TAG) $(IMAGE):latest 2>/dev/null
