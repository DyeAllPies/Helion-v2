PROTO_DIR   := proto
PROTO_FILES := $(wildcard $(PROTO_DIR)/*.proto)

.PHONY: proto build test lint clean

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
	go test -race ./...

lint:
	go vet ./...

clean:
	rm -f $(PROTO_DIR)/*.pb.go
	rm -f $(PROTO_DIR)/*_grpc.pb.go
