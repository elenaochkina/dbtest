BENCH_IMAGE ?= dbtest/bench:dev

.PHONY: build test bench-image

build:
	go build ./...

test:
	go test ./...

# Build the load-generator image. Deliberately a manual step rather than
# something an activity does: building inside a workflow would be slow and
# surprising. Rebuild after changing cmd/bench or anything it imports.
bench-image:
	docker build -t $(BENCH_IMAGE) .
