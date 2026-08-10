.PHONY: build run test vet lint proto docker-up docker-down fmt openapi-lint

build:
	go build -o bin/unigate-server ./cmd/server

run: build
	./bin/unigate-server -config deploy/config/config.local.yaml

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w .

# Regenerate Go code from proto/ratelimit/v1/ratelimit.proto.
# Requires: protoc, protoc-gen-go, protoc-gen-go-grpc on PATH.
proto:
	protoc -I proto \
		--go_out=gen/go --go_opt=paths=source_relative \
		--go-grpc_out=gen/go --go-grpc_opt=paths=source_relative \
		proto/ratelimit/v1/ratelimit.proto

docker-up:
	docker compose -f deploy/docker/docker-compose.yaml up --build

docker-down:
	docker compose -f deploy/docker/docker-compose.yaml down -v

# Validate api/openapi.yaml. Requires: pip install openapi-spec-validator
openapi-lint:
	openapi-spec-validator api/openapi.yaml
