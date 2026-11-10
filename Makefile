BINARY       := bin/server
IMAGE        ?= devcard-bff:dev
COVER_MIN    ?= 80
COVER_FILE   := coverage.out

.DEFAULT_GOAL := help
.PHONY: help run test cover cover-check lint vet build docker docker-run tidy clean

help: ## list targets
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk -F ':.*## ' '{printf "  %-12s %s\n", $$1, $$2}'

run: ## run the server locally (reads PORT, GITHUB_TOKEN, CACHE_TTL, ALLOWED_ORIGIN)
	go run ./cmd/server

test: ## run all tests with the race detector
	go test -race ./...

cover: ## tests + coverage report for internal/ (opens coverage.html)
	go test -race -covermode=atomic -coverprofile=$(COVER_FILE) ./internal/...
	go tool cover -func=$(COVER_FILE) | tail -1
	go tool cover -html=$(COVER_FILE) -o coverage.html

cover-check: ## fail if internal/ coverage is below COVER_MIN percent
	go test -race -covermode=atomic -coverprofile=$(COVER_FILE) ./internal/...
	@total=$$(go tool cover -func=$(COVER_FILE) | awk '/^total:/ {sub("%","",$$3); print $$3}'); \
	echo "internal/ coverage: $$total% (min $(COVER_MIN)%)"; \
	awk -v t="$$total" -v m="$(COVER_MIN)" 'BEGIN { exit (t+0 < m+0) }'

vet: ## go vet
	go vet ./...

lint: ## golangci-lint (config in .golangci.yml)
	golangci-lint run ./...

build: ## static, stripped binary in bin/
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o $(BINARY) ./cmd/server

docker: ## build the container image
	docker build -t $(IMAGE) .

docker-run: docker ## build and run the image on :8080
	docker run --rm -p 8080:8080 -e GITHUB_TOKEN -e ALLOWED_ORIGIN $(IMAGE)

tidy: ## go mod tidy + verify
	go mod tidy
	go mod verify

clean: ## remove build and coverage output
	rm -rf bin $(COVER_FILE) coverage.html
