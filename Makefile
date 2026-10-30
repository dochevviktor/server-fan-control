.PHONY: frontend build test e2e
frontend:
	npm --prefix web ci
	npm --prefix web run check
	npm --prefix web run build
build: frontend
	mkdir -p bin
	CGO_ENABLED=1 go build -trimpath -ldflags="-s -w" -o bin/nvidia-fan-control ./cmd/nvidia-fan-control
test: frontend
	CGO_ENABLED=1 go test -race ./...
	CGO_ENABLED=1 go vet ./...
e2e: build
	npm --prefix web test
