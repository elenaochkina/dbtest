BENCH_IMAGE ?= dbtest/bench:dev
PROBE_IMAGE ?= dbtest/probe:dev

.PHONY: build test images bench-image probe-image

build:
	go build ./...

test:
	go test ./...

images: bench-image probe-image

# Build the load-generator image. Deliberately a manual step rather than
# something an activity does: building inside a workflow would be slow and
# surprising. Rebuild after changing cmd/bench or anything it imports.
#
# -f picks the Dockerfile; the trailing . is the build context, which stays at
# the repo root because the Go build needs the whole module — and because
# .dockerignore belongs to the context, not to the Dockerfile.
bench-image:
	docker build -f build/bench.Dockerfile -t $(BENCH_IMAGE) .

probe-image:
	docker build -f build/probe.Dockerfile -t $(PROBE_IMAGE) .
