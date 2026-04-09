PROTO_DIR   := proto
PROTO_FILES := coordinator.proto node.proto

.PHONY: proto build test test-short lint clean

proto:
	protoc \
		--go_out=$(PROTO_DIR) \
		--go_opt=paths=source_relative \
		--go-grpc_out=$(PROTO_DIR) \
		--go-grpc_opt=paths=source_relative \
		--proto_path=$(PROTO_DIR) \
		$(PROTO_FILES)

build:
	go build ./...

test:
	CGO_ENABLED=0 go test ./internal/... ./tests/integration/...

test-short:
	CGO_ENABLED=0 go test -short ./internal/...

lint:
	go vet ./...

clean:
	rm -f $(PROTO_DIR)/*.pb.go
	rm -f $(PROTO_DIR)/*_grpc.pb.go