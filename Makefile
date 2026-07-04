.PHONY: build run tidy fmt vet test docker up down logs

build:
	CGO_ENABLED=0 go build -ldflags="-s -w" -o bin/searchy ./cmd/bot

run:
	go run ./cmd/bot

tidy:
	go mod tidy

fmt:
	gofmt -w ./cmd ./internal

vet:
	go vet ./...

test:
	go test ./...

docker:
	docker build -t searchy:dev .

up:
	docker compose -f deploy/docker-compose.yml up -d --build

down:
	docker compose -f deploy/docker-compose.yml down

logs:
	docker compose -f deploy/docker-compose.yml logs -f bot
