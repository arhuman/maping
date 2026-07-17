.DEFAULT_GOAL := help

# ==================================================================================== #
# VARIABLES
# ==================================================================================== #
GOOS := $(shell go env GOOS)
GOBIN := $(shell go env GOBIN)
ifeq ($(strip $(GOBIN)),)
GOBIN := $(shell go env GOPATH)/bin
endif
SERVER_MAIN := ./cmd/maping-server

# Docker stacks: one neutral base (docker-compose.yml) + overlays. Both stacks
# read .env for credentials, ports, and auth config.
#   make local -> laptop stack  (host ports published, dev config)
#   make up    -> production stack (only the server port published)
ENV_FILE      := .env
BASE_COMPOSE  := docker compose --env-file $(ENV_FILE) -f docker-compose.yml
LOCAL_COMPOSE := $(BASE_COMPOSE) -f docker-compose.local.yml
PROD_COMPOSE  := $(BASE_COMPOSE) -f docker-compose.prod.yml

# Target a service for logs/restart, e.g. `make logs service=clickhouse`.
service ?= maping-server

ifeq ($(GOOS),windows)
BINARY_NAME := maping-server.exe
else
BINARY_NAME := maping-server
endif

# The repo is a Go workspace (go.work). Every publishable module is listed here
# so test/audit/tidy/checklen iterate over all of them. example/ is intentionally
# excluded (demo code, not a distributed library).
MODULES := proto client client/gin client/nethttp server

# Modules published to proxy.golang.org, in dependency (release) order: a module
# is only tagged after everything it imports is already tagged. Used by `make
# release`. example/ is not published.
RELEASE_MODULES := proto client server client/gin client/nethttp

# Release version for `make release VERSION=vX.Y.Z` (path-prefixed tags per module).
VERSION ?=

LINE_LIMIT ?= 500

# Rounds of sample requests fired by `make generate-traffic` (one round hits
# every example route once).
ROUNDS ?= 20

# ==================================================================================== #
# PHONY DECLARATIONS (in alphabetical order)
# ==================================================================================== #
.PHONY: audit build checklen confirm down generate-traffic help integration local logs proto release restart test tidy tools up

# ==================================================================================== #
# STANDARD TARGETS (in alphabetical order)
# ==================================================================================== #

## audit: run quality control checks across all modules
audit:
	@$(MAKE) checklen
	@which golangci-lint > /dev/null || $(MAKE) tools
	@which govulncheck > /dev/null || $(MAKE) tools
	@for m in $(MODULES); do echo "== audit $$m =="; (cd $$m && go mod verify && golangci-lint run ./... && govulncheck ./...) || exit 1; done

## build: build the server binary
build:
	CGO_ENABLED=0 GOFLAGS="-ldflags=-s -ldflags=-w" go build -o bin/$(BINARY_NAME) ./server/cmd/maping-server

## checklen: fail if any non-generated, non-test Go source file exceeds LINE_LIMIT lines
checklen:
	@bad=""; for f in $$(find $(MODULES) -name '*.go' ! -name '*_test.go' ! -name '*.pb.go' ! -name '*.connect.go'); do \
		n=$$(wc -l < "$$f"); \
		if [ "$$n" -gt $(LINE_LIMIT) ]; then bad="$$bad$$f ($$n)\n"; fi; \
	done; \
	if [ -n "$$bad" ]; then printf "Files over $(LINE_LIMIT) lines:\n$$bad" >&2; exit 1; fi; \
	echo "file length OK (all <= $(LINE_LIMIT) lines)"

## down: stop and remove the running stack (local or prod)
down:
	$(BASE_COMPOSE) down

## generate-traffic: run the example app against the LOCAL stack and fire sample requests so every dashboard panel fills with data (make generate-traffic ROUNDS=30)
generate-traffic: $(ENV_FILE)
	@set -a; . ./$(ENV_FILE) >/dev/null 2>&1; set +a; \
		port="$${MAPING_PORT:-8080}"; \
		case "$$port" in ''|*[!0-9]*) echo "MAPING_PORT must be a number, got '$$port'" >&2; exit 1;; esac; \
		base="http://127.0.0.1:$$port"; \
		key="$${MAPING_KEY:-dev-key}"; \
		curl -fsS -o /dev/null "$$base/" || { echo "local stack unreachable at $$base -- run 'make local' first" >&2; exit 1; }; \
		echo "== building example-api =="; \
		(cd example && go build -o ../bin/example .) || exit 1; \
		echo "== starting example-api, shipping to LOCAL $$base (logs: bin/example.log) =="; \
		MAPING_KEY="$$key" MAPING_ENDPOINT="$$base" ./bin/example >bin/example.log 2>&1 & \
		ex=$$!; trap 'kill $$ex 2>/dev/null; wait $$ex 2>/dev/null' EXIT; \
		for i in $$(seq 1 40); do curl -fsS -o /dev/null http://127.0.0.1:9090/hello/ready 2>/dev/null && break; sleep 0.5; done; \
		echo "== driving $(ROUNDS) rounds across every route =="; \
		for i in $$(seq 1 $(ROUNDS)); do \
			for p in /hello/world /downstream /db-error /upstream-timeout /timeout /canceled /boom; do \
				curl -fsS -o /dev/null "http://127.0.0.1:9090$$p" 2>/dev/null || true; \
			done; \
		done; \
		echo "== waiting ~12s for a flush so summaries ship =="; \
		sleep 12; \
		echo "traffic done -- open $$base to see the data"

## help: display this help message
help:
	@echo 'Usage:'
	@sed -n 's/^##//p' ${MAKEFILE_LIST} | column -t -s ':' | sed -e 's/^/ /'

## proto: lint and regenerate protobuf + connect code
proto: tools
	buf lint
	buf generate

## test: run all tests with race detector across all modules
test:
	@for m in $(MODULES); do echo "== test $$m =="; (cd $$m && go test -race -short -cover ./...) || exit 1; done

## tidy: format code and tidy every module
tidy:
	@for m in $(MODULES); do echo "== tidy $$m =="; (cd $$m && go fmt ./... && go mod tidy) || exit 1; done

## tools: install required Go development tools
tools:
	@echo "Installing Go tools..."
	@go install github.com/bufbuild/buf/cmd/buf@latest
	@go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
	@go install connectrpc.com/connect/cmd/protoc-gen-connect-go@latest
	@go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2
	@go install golang.org/x/vuln/cmd/govulncheck@latest
	@echo "Tools installed in $(GOBIN)"

# ==================================================================================== #
# UTILITY TARGETS
# ==================================================================================== #

## confirm: prompt for user confirmation before proceeding
confirm:
	@echo -n 'Are you sure? [y/N] ' && read ans && [ $${ans:-N} = y ]

# ==================================================================================== #
# PROJECT-SPECIFIC TARGETS
# ==================================================================================== #

## integration: run storage (ClickHouse) and control-plane (Postgres) integration tests
# DSNs point at localhost using the HOST-published ports from .env: compose maps
# Postgres to MAPING_PG_PORT (15432, off 5432 to avoid clashing a local Postgres)
# and ClickHouse native to MAPING_CH_NATIVE_PORT (9000). .env's own MAPING_*_DSN
# use the in-network service hostnames (clickhouse/postgres) and are unreachable
# from the host, so they are NOT reused here — the DSN is rebuilt for localhost.
# An already-exported MAPING_*_DSN still wins; otherwise it is built from .env.
integration:
	@echo "== storage + control integration tests (needs ClickHouse + Postgres; run 'make local' first) =="
	@ch_dsn="$$MAPING_CLICKHOUSE_DSN"; pg_dsn="$$MAPING_POSTGRES_DSN"; \
		set -a; [ -f $(ENV_FILE) ] && . ./$(ENV_FILE) >/dev/null 2>&1; set +a; \
		ch_dsn="$${ch_dsn:-clickhouse://$${MAPING_CH_USER:-maping}:$${MAPING_CH_PASSWORD:-maping}@localhost:$${MAPING_CH_NATIVE_PORT:-9000}/$${MAPING_CH_DB:-maping}}"; \
		pg_dsn="$${pg_dsn:-postgres://$${MAPING_PG_USER:-maping}:$${MAPING_PG_PASSWORD:-maping}@localhost:$${MAPING_PG_PORT:-15432}/$${MAPING_PG_DB:-maping}?sslmode=disable}"; \
		cd server && \
		MAPING_CLICKHOUSE_DSN="$$ch_dsn" \
		MAPING_POSTGRES_DSN="$$pg_dsn" \
		go test -tags=integration -race -cover ./internal/storage/... ./internal/control/...

## local: start the full dev stack (server + ClickHouse + Postgres), host ports published
local: $(ENV_FILE)
	$(LOCAL_COMPOSE) up -d --build
	@echo "Local stack up — dashboard http://localhost:$${MAPING_PORT:-8080}"

## up: start the full production stack (only the server port published)
up: $(ENV_FILE)
	$(PROD_COMPOSE) up -d --build
	@echo "Production stack up."

## release: tag + publish all modules at VERSION in dependency order (make release VERSION=v0.1.0)
release: confirm
	@test -n "$(VERSION)" || { echo "VERSION is required, e.g. make release VERSION=v0.1.0" >&2; exit 1; }
	@echo "$(VERSION)" | grep -qE '^v[0-9]+\.[0-9]+\.[0-9]+' || { echo "VERSION must look like vX.Y.Z" >&2; exit 1; }
	@test -z "$$(git status --porcelain)" || { echo "working tree not clean; commit or stash first" >&2; exit 1; }
	@echo "== 1/3 proto (no internal deps) =="
	git tag proto/$(VERSION)
	git push origin proto/$(VERSION)
	@echo "== 2/3 client + server: pin proto@$(VERSION), drop local replace =="
	cd client && go mod edit -dropreplace=github.com/arhuman/maping/proto -require=github.com/arhuman/maping/proto@$(VERSION) && go mod tidy
	cd server && go mod edit -dropreplace=github.com/arhuman/maping/proto -require=github.com/arhuman/maping/proto@$(VERSION) && go mod tidy
	git commit -am "chore(release): client & server $(VERSION) on proto@$(VERSION)"
	git tag client/$(VERSION) && git tag server/$(VERSION)
	git push origin client/$(VERSION) server/$(VERSION)
	@echo "== 3/3 client/gin: pin client@$(VERSION) + proto@$(VERSION), drop local replaces =="
	cd client/gin && go mod edit \
		-dropreplace=github.com/arhuman/maping/client -dropreplace=github.com/arhuman/maping/proto \
		-require=github.com/arhuman/maping/client@$(VERSION) -require=github.com/arhuman/maping/proto@$(VERSION) && go mod tidy
	git commit -am "chore(release): client/gin $(VERSION)"
	git tag client/gin/$(VERSION)
	git push origin client/gin/$(VERSION)
	@echo "released: proto/$(VERSION) client/$(VERSION) server/$(VERSION) client/gin/$(VERSION)"
	@echo "verify as a consumer: (cd /tmp && GOWORK=off go get github.com/arhuman/maping/client@$(VERSION))"

## logs: follow logs of a service (make logs service=clickhouse)
logs:
	$(BASE_COMPOSE) logs -f $(service)

## restart: restart a service in the local stack (make restart service=maping-server)
restart:
	$(LOCAL_COMPOSE) restart $(service)

# Bootstrap .env from the template on first use so make local / make up work
# out of the box. Never overwrites an existing .env.
$(ENV_FILE):
	@cp env.sample $(ENV_FILE)
	@echo "Created $(ENV_FILE) from env.sample — review it (secrets, OIDC) before prod."
