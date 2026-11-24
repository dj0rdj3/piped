.PHONY: setup frontend frontend-dev web-dev build build-dev docker-binary docker-image lint-docker dev test lint format check check-vuln check-setup check-databases clean clean-cache

export GOCACHE := $(CURDIR)/.cache/go-build
export GOPATH := $(CURDIR)/.cache/go
export npm_config_cache := $(CURDIR)/.cache/npm
export CGO_ENABLED := 0
export PATH := $(CURDIR)/.tools/go/bin:$(CURDIR)/.tools/node/bin:$(PATH)

GO_PACKAGES := ./cmd/... ./internal/... ./web ./scripts/check-databases ./scripts/setup
GOVULNCHECK := go run golang.org/x/vuln/cmd/govulncheck@$(shell cat scripts/govulncheck.version)
DOCKER_PLATFORM ?= linux/amd64
DOCKER_IMAGE ?= piped:local

setup:
	sh scripts/setup.sh

frontend:
	npm --prefix web run build

build: frontend
	mkdir -p bin
	CGO_ENABLED=0 go build -buildvcs=false -trimpath -o bin/piped ./cmd/piped
	npm --prefix web run check:production -- ../bin/piped
	$(GOVULNCHECK) -mode=binary bin/piped

docker-binary:
	docker buildx build --no-cache-filter checked --platform $(DOCKER_PLATFORM) --target binary --output type=local,dest=bin .

docker-image:
	docker buildx build --no-cache-filter checked --platform $(DOCKER_PLATFORM) --load --tag $(DOCKER_IMAGE) .

lint-docker:
	docker buildx build --check .

frontend-dev:
	npm --prefix web run build:development

web-dev:
	npm --prefix web run dev

build-dev: frontend-dev
	mkdir -p bin
	CGO_ENABLED=0 go build -buildvcs=false -tags development -trimpath -o bin/piped-dev ./cmd/piped
	npm --prefix web run check:development -- ../bin/piped-dev
	$(GOVULNCHECK) -mode=binary bin/piped-dev

dev: frontend-dev
	CGO_ENABLED=0 go run -buildvcs=false -tags development ./cmd/piped --development --listen 127.0.0.1:8080 --public-url http://127.0.0.1:5173

test: frontend
	CGO_ENABLED=0 go test $(GO_PACKAGES)
	CGO_ENABLED=0 go test -tags development $(GO_PACKAGES)
	CGO_ENABLED=1 go test -race $(GO_PACKAGES)
	CGO_ENABLED=1 go test -tags development -race $(GO_PACKAGES)
	npm --prefix web test

lint:
	shellcheck scripts/*.sh
	@test -z "$$(gofmt -l cmd internal web/embed.go scripts)" || (gofmt -l cmd internal web/embed.go scripts; exit 1)
	CGO_ENABLED=0 go vet $(GO_PACKAGES)
	CGO_ENABLED=0 go vet -tags development $(GO_PACKAGES)
	npm --prefix web run lint
	npm --prefix web run format:check

format:
	gofmt -w cmd internal web/embed.go scripts
	npm --prefix web run format

check-vuln: frontend
	$(GOVULNCHECK) -test $(GO_PACKAGES)
	$(GOVULNCHECK) -test -tags development $(GO_PACKAGES)

check: lint test check-vuln
	npm --prefix web run check:production
	npm --prefix web run check:tooling

check-databases: frontend
	go run ./scripts/check-databases

check-setup:
	PIPED_SETUP_CONTAINER_TESTS=1 go test -v -parallel 2 -timeout 90m -run '^TestSetupContainers$$' ./scripts/setup

clean:
	rm -rf bin web/dist

clean-cache:
	@if [ -d .cache ] && [ ! -L .cache ]; then chmod -R u+w .cache; fi
	rm -rf .cache
