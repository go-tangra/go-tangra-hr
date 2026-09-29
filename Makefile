GO        ?= go
PKGS      := $(shell $(GO) list ./... | grep -v /ui/)
COVER_OUT := coverage.out

.PHONY: lint vuln test test-integration cover fuzz ui-build build build-ui image

lint:
	$(GO) vet $(PKGS)
	staticcheck $(PKGS)
	gosec -quiet -exclude-generated -exclude-dir=ui ./...

vuln:
	./scripts/vulncheck.sh

test:
	$(GO) test -race -count=1 $(PKGS)

# Docker-backed suites (testcontainers) carry the integration build tag next to
# the code they exercise.
test-integration:
	$(GO) test -race -count=1 -tags integration ./internal/repo/repodb/ ./tests/integration/

# Generated protobuf, SQL bindings (internal/store, */*db), wiring (internal/app,
# cmd) and test packages are exercised by the tagged integration suite and are
# excluded from the unit gate on purpose.
COVERPKG := $(shell $(GO) list ./... | grep -v -E '/api/|/internal/store$$|db$$|/internal/app$$|/valkeykv$$|/cmd/|/tests/|/ui|/internal/stream' | paste -sd, -)

cover:
	$(GO) test -count=1 -coverprofile=$(COVER_OUT) -coverpkg=$(COVERPKG) $(PKGS)
	./scripts/coverage-gate.sh $(COVER_OUT)

# Every fuzz target runs for FUZZTIME (package:target pairs).
FUZZTIME ?= 10s
fuzz:
	@set -e; for f in $$(grep -rlE '^func Fuzz' --include='*_test.go' internal); do \
		pkg=./$$(dirname $$f); \
		for name in $$(grep -hoE '^func (Fuzz[A-Za-z0-9_]+)' $$f | sed 's/func //'); do \
			echo "fuzz $$pkg $$name"; \
			$(GO) test -run '^$$' -fuzz "^$$name$$" -fuzztime $(FUZZTIME) $$pkg; \
		done; \
	done

# Build the federated UI remote (produces ui/dist consumed by the -tags ui build).
ui-build:
	cd ui && npm ci && npm run build

# Build the service binary without the embedded UI.
build:
	$(GO) build -o bin/hrsvc ./cmd/hrsvc

# Build the service binary with the embedded UI remote (requires ui-build first).
build-ui: ui-build
	$(GO) build -tags "ui" -o bin/hrsvc ./cmd/hrsvc

# Build the container image; NODE_AUTH_TOKEN (read:packages) installs @go-tangra/ui.
image:
	DOCKER_BUILDKIT=1 docker buildx build --secret id=npm_token,env=NODE_AUTH_TOKEN -t go-tangra-hr:dev .
