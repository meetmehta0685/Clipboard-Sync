.PHONY: help up down restart status logs demo all build web test test-go test-web vectors dev-relay clean

help:
	@scripts/clipsync help
	@echo
	@echo "Also: make all (build everything), make test, make clean"

up down restart status demo:
	@scripts/clipsync $@

# make logs, or make logs s=relay|daemon|tray
logs:
	@scripts/clipsync logs $(s)

all: web build

build:
	go build -o bin/clipsyncd ./cmd/clipsyncd
	go build -o bin/relay ./cmd/relay
	go build -o bin/clipsync-tray ./cmd/clipsync-tray

web:
	cd web && pnpm install --frozen-lockfile && pnpm build

test: test-go test-web

test-go:
	go vet ./...
	go test -race ./...

# Cross-language crypto check: Go writes vectors, JS verifies them and
# writes its own, Go verifies those.
test-web:
	cd web && pnpm test
	go test ./internal/crypto -run JSVectors

vectors:
	go test ./internal/crypto -run Vectors -update
	cd web && pnpm test

dev-relay: web
	go run ./cmd/relay -web web/out

clean:
	rm -rf bin dist web/out web/.next
