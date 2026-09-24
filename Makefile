# Registry settings live in config.mk, which is not committed.
# See config.mk.example.
-include config.mk

REGISTRY ?=
TAG      ?= dev
PLATFORM ?= linux/arm64

BENCH_IMAGE ?= dbtest/bench:dev
PROBE_IMAGE ?= dbtest/probe:dev

.PHONY: build test images bench-image probe-image registry-login push push-bench push-probe

build:
	go build ./...

test:
	go test ./...

images: bench-image probe-image

# Build the load-generator image. 
bench-image:
	docker build -f build/bench.Dockerfile -t $(BENCH_IMAGE) .

probe-image:
	docker build -f build/probe.Dockerfile -t $(PROBE_IMAGE) .

# Push to whichever registry config.mk names. The login token is short-lived,
# so push depends on it rather than assuming a prior login.
registry-login:
	@$(if $(REGISTRY_LOGIN),,$(error REGISTRY_LOGIN is unset - see config.mk.example))
	$(REGISTRY_LOGIN)

push: registry-login push-bench push-probe

push-bench:
	@$(if $(REGISTRY),,$(error REGISTRY is unset - see config.mk.example))
	docker build --platform $(PLATFORM) -f build/bench.Dockerfile -t $(REGISTRY)/bench:$(TAG) .
	docker push $(REGISTRY)/bench:$(TAG)

push-probe:
	@$(if $(REGISTRY),,$(error REGISTRY is unset - see config.mk.example))
	docker build --platform $(PLATFORM) -f build/probe.Dockerfile -t $(REGISTRY)/probe:$(TAG) .
	docker push $(REGISTRY)/probe:$(TAG)
