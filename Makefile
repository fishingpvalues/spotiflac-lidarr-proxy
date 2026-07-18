.PHONY: build test lint run docker-build docker-up docker-down clean

APP := spotiflac-lidarr-proxy
BIN := server

build:
	go build -ldflags="-s -w" -o $(BIN) ./cmd/server

test:
	go test ./... -count=1

test-race:
	go test -race ./... -count=1

test-integration:
	INTEGRATION=1 go test ./tests/integration/... -v -count=1

test-cover:
	go test -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out
	go tool cover -html=coverage.out -o coverage.html

lint:
	golangci-lint run

fmt:
	gofmt -s -w .

run: build
	./$(BIN) serve

docker-build:
	docker build -t $(APP):dev .

docker-up:
	docker compose up -d

docker-down:
	docker compose down

docker-logs:
	docker compose logs -f

clean:
	rm -f $(BIN) coverage.out coverage.html

deps:
	go mod tidy
	go mod verify
