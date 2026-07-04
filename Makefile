MODULE  := github.com/dvrkn/khook
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w \
	-X $(MODULE)/internal/cli.Version=$(VERSION) \
	-X $(MODULE)/internal/cli.Commit=$(COMMIT) \
	-X $(MODULE)/internal/cli.Date=$(DATE)

.PHONY: build dev test e2e schema

# Release binary: static (CGO off), stripped, version-stamped. GOOS/GOARCH
# pass through from the environment for cross-builds.
build:
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/khook ./cmd/khook
	@ls -lh bin/khook | awk '{print "bin/khook: " $$5}'

# Fast unstripped build for local iteration.
dev:
	go build -o bin/khook ./cmd/khook

test:
	go test ./...

e2e:
	./tests/e2e.sh

# Regenerate the committed JSON Schema from the spec types. docs/ is the single
# source of truth: GitHub Pages (serving /docs) publishes the schema's $id URL,
# https://khook.io/schema/v1/khook.json, from there. schema_test.go fails when
# the committed artifact drifts from the types.
schema:
	mkdir -p docs/schema/v1
	go run ./cmd/khook schema > docs/schema/v1/khook.json
