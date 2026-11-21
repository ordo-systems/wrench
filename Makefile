# Wrench: the Go server at the root, the Vue dashboard in webapp/, the image, the deployments in deploy/, the
# black-box tests in tests/ and the website in website/. `make` lists every target and tunable variable.

# Every recipe runs in bash with errors, unset variables and failed pipes fatal (in GNU Make 3.81 too).
SHELL         := bash -euo pipefail
.DEFAULT_GOAL := help
.DELETE_ON_ERROR:
MAKEFLAGS     += --no-builtin-rules --no-print-directory
export CGO_ENABLED ?= 0

##@ Variables (override on the command line: make go-test RUN=TestBudget PKG=./internal/budget)
PKG          ?= ./...## Go packages for go-test, test-race and cover
RUN          ?=## Go test name filter (go test -run)
TEST_PG_PORT ?= 55432## Port of the disposable test PostgreSQL
SCAN_IMAGE   ?= wrench:dev## Image that image-scan checks
MUTATE_FILES ?=## Files mutate changes (required, keep it small)
WRENCH_URL   ?=## Server livecheck runs against (with WRENCH_TOKEN in the environment)

GO           := go
WEB          := webapp
VERSION      := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT       := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS      := -X github.com/ordo-systems/wrench/internal/version.Version=$(VERSION) \
                -X github.com/ordo-systems/wrench/internal/version.Commit=$(COMMIT)
# Compose builds in deploy/ stamp the image with the same metadata.
export WRENCH_BUILD_VERSION ?= $(VERSION)
export WRENCH_BUILD_COMMIT  ?= $(COMMIT)

# Disposable PostgreSQL for the Go tests; never point it at a real database.
TEST_PG      := wrench-test-pg
TEST_ENV     := WRENCH_TEST_POSTGRES_DSN='postgres://wrench:wrench@127.0.0.1:$(TEST_PG_PORT)/wrench?sslmode=disable'
GO_TEST      := $(GO) test -count=1 $(if $(RUN),-run '$(RUN)')
COVER        := target/coverage/go/coverage.out

# Pinned tools, run without installing them.
GOLANGCI     := $(GO) run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0
GOVULNCHECK  := $(GO) run golang.org/x/vuln/cmd/govulncheck@v1.9.0
ACTIONLINT   := $(GO) run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12
TRIVY        := docker run --rm -v /var/run/docker.sock:/var/run/docker.sock -v $(CURDIR)/target/trivy:/root/.cache/trivy \
                -v $(CURDIR)/.trivyignore.yaml:/.trivyignore.yaml:ro \
                aquasec/trivy:0.76.2@sha256:8f6bb947dbc528902e11f2f5be52f7b519f6a39d5d148f41166f2040499768fb

# The demo stack; e2e and opencode-contract run it under their own projects so they never touch a running demo.
DEMO         := docker compose -f deploy/demo/docker-compose.yaml
# with-stack runs $(2) against a fresh demo stack named $(1) and removes the stack afterwards, even when starting it
# or the command fails.
with-stack    = trap '$(DEMO) -p $(1) down -v' EXIT; $(DEMO) -p $(1) up -d --build --wait; $(2)

API_GENERATED := api/api.gen.go $(WEB)/src/api.gen.ts tests/internal/wrenchapi/wrenchapi.gen.go

# Pattern rules never match phony targets, so print-% stays out.
.PHONY: $(filter-out print-%,$(MAKECMDGOALS))

##@ Develop

help: ## Show targets and variables
	@if [[ -t 1 && -z "$${NO_COLOR:-}" ]]; then b=$$'\e[1m'; c=$$'\e[36m'; r=$$'\e[0m'; else b= c= r=; fi; \
	awk -v b="$$b" -v c="$$c" -v r="$$r" ' \
		/^##@ Variables/ { vars = 1; next } \
		/^##@/ { vars = 0; printf "\n%s%s%s\n", b, substr($$0, 5), r; next } \
		vars && /^[A-Z_]+ +[?]=/ { split($$0, p, "##"); i = index(p[1], "?="); n = substr(p[1], 1, i - 1); \
			d = substr(p[1], i + 2); gsub(/ /, "", n); gsub(/^ +| +$$/, "", d); \
			v = v sprintf("  %s%-14s%s %s%s\n", c, n, r, p[2], d == "" ? "" : " [" d "]") } \
		/^[a-z0-9%-]+:.*## / { split($$0, p, ":.*## "); printf "  %s%-18s%s %s\n", c, p[1], r, p[2] } \
		END { printf "\n%sVariables%s\n%s", b, r, v }' $(MAKEFILE_LIST)

doctor: ## Check the tools this repository needs
	@missing=0; \
	check() { if command -v "$$1" >/dev/null; then printf '  ok    %-9s %s\n' "$$1" "$$($$2 2>&1 | head -1)"; \
		elif [[ $$3 == required ]]; then printf '  MISS  %-9s required\n' "$$1"; missing=1; \
		else printf '  --    %-9s optional, for %s\n' "$$1" "$$3"; fi; }; \
	check go "go version" required; check node "node --version" required; check npm "npm --version" required; \
	check docker "docker compose version" required; check git "git --version" required; \
	check python3 "python3 --version" required; check opencode "opencode --version" "make opencode-contract"; \
	check crapper "crapper --version" "make crap"; check dryer "dryer --version" "make dry"; \
	check mutator "mutator --version" "make mutate"; \
	exit $$missing

deps: ## Download Go modules and install dashboard dependencies
	$(GO) mod download
	cd $(WEB) && npm ci

fmt: ## Format Go and dashboard sources
	gofmt -w .
	cd $(WEB) && npm run format

generate: ## Regenerate API code, dashboard types and the tests' client from api/openapi.yaml
	cd api && $(GO) tool oapi-codegen -config oapi-codegen.yaml openapi.yaml
	cd tests/internal/wrenchapi && $(GO) tool oapi-codegen -config oapi-codegen.yaml ../../../api/openapi.yaml
	{ echo '// Code generated by openapi-typescript from api/openapi.yaml. DO NOT EDIT.'; \
		$(WEB)/node_modules/.bin/openapi-typescript api/openapi.yaml; } > $(WEB)/src/api.gen.ts.tmp
	mv $(WEB)/src/api.gen.ts.tmp $(WEB)/src/api.gen.ts

web: ## Build the dashboard into the server's embed directory
	cd $(WEB) && npm run build

build: web ## Build the dashboard and bin/wrench
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o bin/wrench ./cmd/wrench

clean: ## Remove build, coverage and scan outputs
	rm -rf bin target
	find internal/web/dist -mindepth 1 -not -name .gitkeep -delete

print-%: ## Print a variable: make print-VERSION
	@echo '$($*)'

##@ Test

test: go-test web-test ## Go tests (with PostgreSQL) and dashboard tests

go-test: test-db-up ## Go tests; narrow with PKG= and RUN=
	$(TEST_ENV) $(GO_TEST) $(PKG)

web-test: ## Dashboard tests
	cd $(WEB) && npm test

test-race: test-db-up ## Go tests with the race detector (enables CGO)
	CGO_ENABLED=1 $(TEST_ENV) $(GO_TEST) -race $(PKG)

cover: test-db-up ## Go coverage profile in target/coverage/go and its total
	mkdir -p $(dir $(COVER))
	$(TEST_ENV) $(GO_TEST) -coverprofile=$(COVER) $(PKG)
	$(GO) tool cover -func=$(COVER) | tail -1

e2e: ## End-to-end tests against a disposable demo stack (Docker)
	$(call with-stack,wrench-e2e,WRENCH_E2E_URL=http://127.0.0.1:18080 WRENCH_E2E_OPENROUTER_URL=http://127.0.0.1:18000 \
		WRENCH_E2E_GITLAB_URL=http://127.0.0.1:19000 $(GO) test -count=1 -timeout 5m -tags integration ./tests/e2e/...)

opencode-contract: ## The installed opencode 2.3.x through device login and a run (Docker)
	$(call with-stack,wrench-oc,WRENCH_OC_URL=http://localhost:18080 WRENCH_OC_GITLAB_URL=http://127.0.0.1:19000 \
		$(GO) test -count=1 -timeout 5m -tags opencode ./agent/opencode/...)

backup-drill: ## Back up to a stand-in S3, destroy everything, restore (Docker)
	./tests/backup-drill/drill.sh

livecheck: ## Pilot check against WRENCH_URL with WRENCH_TOKEN (costs a few requests per model)
	@[[ -n "$(WRENCH_URL)" ]] || { echo 'set WRENCH_URL'; exit 1; }
	$(GO) run ./tests/cmd/wrench-livecheck -url $(WRENCH_URL)

test-db-up: ## Start the disposable test PostgreSQL
	@docker inspect -f '{{.State.Running}}' $(TEST_PG) 2>/dev/null | grep -q true || \
		docker run -d --rm --name $(TEST_PG) -e POSTGRES_USER=wrench -e POSTGRES_PASSWORD=wrench -e POSTGRES_DB=wrench \
			-p 127.0.0.1:$(TEST_PG_PORT):5432 postgres:17-alpine >/dev/null
	@for _ in {1..30}; do docker exec $(TEST_PG) pg_isready -U wrench -q && exit 0; sleep 1; done; \
		echo 'test PostgreSQL did not become ready'; exit 1

test-db-down: ## Remove the test PostgreSQL
	@docker rm -f $(TEST_PG) >/dev/null 2>&1 || true

##@ Lint

lint: lint-workflows go-lint web-lint check-generated check-blackbox site-check ## Every linter

go-lint: ## gofmt, go vet and golangci-lint (gosec included)
	@out=$$(gofmt -l .); [[ -z $$out ]] || { echo "gofmt needed:"; echo "$$out"; exit 1; }
	$(GO) vet -tags integration,opencode ./...
	$(GOLANGCI) run ./...

web-lint: ## Type-check the dashboard and check its formatting
	cd $(WEB) && npm run lint

# actionlint, every third-party action pinned to a commit SHA with its version in a comment, and the scripts' tests.
lint-workflows: ## GitHub Actions: actionlint, SHA pins, release scripts
	$(ACTIONLINT) -color
	@bad=$$(grep -nE '^\s*(-\s*)?uses:\s*[^.[:space:]]' .github/workflows/*.yml | grep -vE '@[0-9a-f]{40} # v[0-9]' || true); \
	[[ -z $$bad ]] || { echo "actions must be pinned to a commit SHA with a version comment:"; echo "$$bad"; exit 1; }
	@grep -q 'GITHUB_REPOSITORY_OWNER,,' .github/workflows/image-build.yml || \
		{ echo 'image-build.yml must lower-case the image owner (GHCR rejects upper case)'; exit 1; }
	@sh .github/scripts/release-tag_test.sh
	@sh .github/scripts/workflows_test.sh

# The files on disk must be exactly what the document produces, committed or not.
check-generated: ## Fail when generated API code is out of date
	@before=$$(for f in $(API_GENERATED); do [[ -f $$f ]] && shasum $$f || echo "missing $$f"; done); \
	$(MAKE) generate >/dev/null; \
	after=$$(shasum $(API_GENERATED)); \
	[[ $$before == "$$after" ]] || { echo 'generated API code was out of date; review and commit:'; \
		git status --short -- $(API_GENERATED); exit 1; }

# tests/ and agent/ talk to a running Wrench through the JSON API only; one module cannot forbid the imports itself.
check-blackbox: ## Fail when tests/ or agent/ import the server's internal/ or cmd/
	@bad=$$($(GO) list -test -tags integration,opencode -deps ./tests/... ./agent/... \
		| grep -E '^github.com/ordo-systems/wrench/(internal|cmd)/' || true); \
	[[ -z $$bad ]] || { echo 'tests/ and agent/ must use only the JSON API; they import:'; echo "$$bad"; exit 1; }

vuln: ## Known vulnerabilities in Go and npm dependencies (npm: high and above)
	$(GOVULNCHECK) ./...
	cd $(WEB) && npm audit --audit-level=high

tidy: ## Fail when go mod tidy changes go.mod or go.sum
	GOWORK=off $(GO) mod tidy
	git diff --exit-code -- go.mod go.sum

ci: deps lint test build ## What CI's checks job runs

##@ Image

image: ## Build wrench:dev
	docker build --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) -t wrench:dev .

image-smoke: ## Check the tools and the binary inside wrench:dev
	docker run --rm --entrypoint pg_dump wrench:dev --version | grep -F 'PostgreSQL) 17.'
	docker run --rm --entrypoint pg_restore wrench:dev --version | grep -F 'PostgreSQL) 17.'
	docker run --rm --entrypoint restic wrench:dev version | grep -F 'restic 0.20.0'
	docker run --rm wrench:dev version | grep -E '^wrench $(VERSION) '

image-scan: ## Fail on HIGH or CRITICAL vulnerabilities in SCAN_IMAGE (Trivy)
	mkdir -p target/trivy
	$(TRIVY) image --scanners vuln --severity HIGH,CRITICAL --ignorefile /.trivyignore.yaml --exit-code 1 \
		--no-progress $(SCAN_IMAGE)

##@ Run

demo: ## Demo with seeded data on http://localhost:18080
	$(DEMO) up -d --build --wait
	$(DEMO) run --rm --build seed
	@echo 'Wrench demo: http://localhost:18080 (sign in through the GitLab mock picker)'

demo-down: ## Stop the demo, keeping its data
	$(DEMO) --profile seed down

demo-reset: ## Stop the demo and delete its data
	$(DEMO) --profile seed down -v

demo-logs: ## Follow the demo logs
	$(DEMO) logs -f

prod-config: ## Validate deploy/production against its .env (or env.example)
	docker compose --env-file $(or $(wildcard deploy/production/.env),deploy/production/env.example) \
		-f deploy/production/docker-compose.yaml config --quiet

site: ## Serve the website on http://127.0.0.1:8000
	@$(MAKE) -C website serve

site-check: ## Check the website's links and shared navigation
	@$(MAKE) -C website check

##@ Quality (crapper, dryer and mutator, installed outside the repository)

crap: cover ## CRAP scores from real coverage (aim below 10)
	crapper --use-existing-coverage -s .

dry: ## Duplicate candidates at threshold 0.8
	dryer --threshold 0.8 api cmd internal tests $(WEB)/src

# mutator cannot map Go import-path coverage keys to files, so every selected site runs.
mutate: test-db-up ## Mutation testing on MUTATE_FILES
	@[[ -n "$(MUTATE_FILES)" ]] || { echo 'set MUTATE_FILES to a bounded file selection'; exit 1; }
	mutator --no-coverage --test-command "$(TEST_ENV) $(GO) test -count=1 ./..." $(MUTATE_FILES)
