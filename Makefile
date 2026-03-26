.PHONY: generate build run dev docker-build docker-up docker-logs hash-password

generate:
	templ generate
	sqlc generate

build: generate
	go build -o bin/server ./cmd/server

run:
	go run ./cmd/server

dev:
	@echo "Run 'templ generate --watch' in one terminal"
	@echo "Run 'go run ./cmd/server' in another"

docker-build:
	docker compose build

docker-up:
	docker compose up -d

docker-down:
	docker compose down

docker-logs:
	docker compose logs -f

hash-password:
	@read -p "Password: " pwd; \
	go run -mod=mod ./hack/hashpw "$$pwd"

tidy:
	go mod tidy
