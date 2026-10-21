.PHONY: tidy run build test test-db vet docker

tidy:
	go mod tidy

run:
	go run ./cmd/bot

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/chainwatch ./cmd/bot

test:
	go test -race -count=1 ./...

test-db:
	TEST_DATABASE_URL=postgres://chainwatch:chainwatch@localhost:5432/chainwatch?sslmode=disable go test -count=1 ./internal/store/...

vet:
	go vet ./...

docker:
	docker build -t chainwatch-bot .
