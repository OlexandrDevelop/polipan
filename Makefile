# SPDX-License-Identifier: Apache-2.0
#
# Developer and CI entry points. CI calls these targets, so a gate runs the same way on your machine.

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
GOOS    ?= $(shell go env GOOS)
GOARCH  ?= $(shell go env GOARCH)

MOD := github.com/polipan/polipan
LDFLAGS = -s -w
BUF  := go run github.com/bufbuild/buf/cmd/buf@v1.47.2
STATICCHECK := go run honnef.co/go/tools/cmd/staticcheck@2025.1.1
GOLANGCI := go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.1.6
REDOCLY := npx --yes @redocly/cli@1.34.20

.PHONY: help
help: ## Show this list
	@grep -E '^[a-zA-Z_-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-16s %s\n", $$1, $$2}'

# ---- Build ----

.PHONY: build
build: web build-go ## Build web, server, agent and cli into ./bin

.PHONY: build-go
build-go: ## Build the three Go binaries into ./bin (set GOOS and GOARCH to cross-compile)
	@mkdir -p bin/$(GOOS)-$(GOARCH)
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) go build -trimpath -ldflags "$(LDFLAGS) -X $(MOD)/server/internal/version.Version=$(VERSION) -X $(MOD)/server/internal/version.Commit=$(COMMIT)" -o bin/$(GOOS)-$(GOARCH)/polipan-server ./server/cmd/polipan-server
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) go build -trimpath -ldflags "$(LDFLAGS) -X $(MOD)/agent/internal/version.Version=$(VERSION) -X $(MOD)/agent/internal/version.Commit=$(COMMIT)" -o bin/$(GOOS)-$(GOARCH)/polipan-agent ./agent/cmd/polipan-agent
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) go build -trimpath -ldflags "$(LDFLAGS) -X $(MOD)/cli/internal/version.Version=$(VERSION) -X $(MOD)/cli/internal/version.Commit=$(COMMIT)" -o bin/$(GOOS)-$(GOARCH)/polipan ./cli/cmd/polipan

.PHONY: web
web: ## Build the web app into server/internal/web/dist (embedded in the server)
	cd web && npm ci --no-audit --no-fund && npm run build

.PHONY: images
images: ## Build the server, agent and cli images (tag polipan-*:dev)
	docker build -f deploy/docker/server.Dockerfile --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) -t polipan-server:dev .
	docker build -f deploy/docker/agent.Dockerfile --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) -t polipan-agent:dev .
	docker build -f deploy/docker/cli.Dockerfile --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) -t polipan-cli:dev .

.PHONY: smoke
smoke: ## Start the images and check /healthz (gate 11, needs make images)
	sh test/ci/smoke.sh polipan-server:dev polipan-agent:dev polipan-cli:dev

.PHONY: release-test
release-test: ## Tests of the release scripts and the reproducible build (needs Go and git, no network)
	sh test/release/test.sh --build

.PHONY: upgrade-test
upgrade-test: ## Upgrade test: the new version on the data of the previous pre-release (needs Docker). make upgrade-test FROM=<tag or commit>
	sh test/upgrade/run.sh $(if $(FROM),--from $(FROM),)

# ---- Generated code ----

.PHONY: api-sync
api-sync: ## Copy openapi.yaml and agent.proto from docs/spec to /api and /proto
	cp docs/spec/openapi.yaml api/openapi.yaml
	cp docs/spec/schemas/agent.proto proto/agent.proto
	cp docs/spec/schemas/polipan.schema.json api/polipan.schema.json

.PHONY: generate
generate: ## Generate Go and TypeScript API code and the gRPC code
	sh api/generate.sh
	cd web && npm run generate:api
	cd proto && $(BUF) generate
	gofmt -w api/gen proto/agentv1

.PHONY: generate-check
generate-check: generate ## Gate 6: generated code must be up to date
	@test -z "$$(git status --porcelain -- api/gen proto/agentv1)" || { echo "Generated code is old. Run make generate and commit."; git status --short -- api/gen proto/agentv1; exit 1; }

# ---- Tests ----

.PHONY: test
test: ## Unit, component and contract tests (needs POLIPAN_TEST_DATABASE_URL)
	go test ./...

# ---- CI gates (numbers are from docs/spec/13-testing-and-quality.md) ----

.PHONY: lint
lint: lint-go lint-web ## Gate 1: Go and web lint and format checks

.PHONY: lint-go
lint-go:
	@test -z "$$(gofmt -l agent api cli proto server test)" || { echo "gofmt needed:"; gofmt -l agent api cli proto server test; exit 1; }
	go vet ./...
	# staticcheck reports style problems in generated code, so the generated package is left out.
	$(STATICCHECK) $$(go list ./... | grep -v '/api/gen$$')
	$(GOLANGCI) run ./...

.PHONY: lint-web
lint-web:
	cd web && npm ci --no-audit --no-fund && npm run lint && npm run typecheck && npm run format:check

.PHONY: gate3
gate3: ## Gate 3: OpenAPI lint, breaking-change check and screens operationId check
	sh test/ci/check-sync.sh
	sh test/ci/check-screens-operations.sh
	$(REDOCLY) lint api/openapi.yaml
	sh test/ci/openapi-breaking.sh

.PHONY: gate4
gate4: ## Gate 4: x-permission check and route check (REQ-API-101)
	go test ./server/internal/api -run 'REQ_API_101'

.PHONY: gate5
gate5: ## Gate 5: schema checks
	go test ./test/schemas

.PHONY: gate6
gate6: generate-check ## Gate 6: proto compiles, lint, generated code, buf breaking
	cd proto && $(BUF) lint && $(BUF) build
	sh test/ci/proto-breaking.sh

.PHONY: gate7
gate7: ## Gate 7: migrations apply on an empty database and match the spec DDL (needs POLIPAN_TEST_DATABASE_URL)
	go test ./server/internal/db -run 'TestM0_FirstMigrationMatchesSpecDDL'

.PHONY: gate8
gate8: ## Gate 8: license check and SPDX headers
	sh test/ci/check-spdx.sh
	sh test/ci/check-licenses-go.sh
	sh test/ci/check-licenses-web.sh

.PHONY: vuln
vuln: ## Dependency scan: govulncheck for the server and the Terraform provider, npm audit for the web runtime packages
	go run golang.org/x/vuln/cmd/govulncheck@v1.1.4 ./...
	cd terraform-provider && go run golang.org/x/vuln/cmd/govulncheck@v1.1.4 ./...
	cd web && npm ci --no-audit --no-fund && npm audit --omit=dev --audit-level=high

.PHONY: fuzz
fuzz: ## Fuzz the parsers of untrusted input, FUZZTIME each (default 20s). Needs no database.
	@for t in cac:FuzzParse tpl:FuzzImportCompose tpl:FuzzParseManifest git:FuzzWebhook obs:FuzzTextFilter sso:FuzzVerifyIDToken; do \
		go test ./server/internal/$${t%%:*}/ -run '^$$' -fuzz "^$${t##*:}$$" -fuzztime $(or $(FUZZTIME),20s) || exit 1; done

.PHONY: gates
gates: lint gate3 gate4 gate5 gate6 gate8 ## All gates that need no database
