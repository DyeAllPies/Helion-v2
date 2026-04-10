module github.com/DyeAllPies/Helion-v2

go 1.23

require (
	github.com/cloudflare/circl v1.6.3
	github.com/dgraph-io/badger/v4 v4.3.0
	github.com/golang-jwt/jwt/v5 v5.2.0
	github.com/google/uuid v1.6.0
	github.com/gorilla/websocket v1.5.1
	golang.org/x/time v0.5.0
	google.golang.org/grpc v1.64.0
	google.golang.org/protobuf v1.34.2
)

require github.com/golang/protobuf v1.5.4 // indirect

// Indirect dependencies pulled in by BadgerDB v4 and gRPC.
// Run `go mod tidy` after first build to populate go.sum.
require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/dgraph-io/ristretto v1.0.0 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/gogo/protobuf v1.3.2 // indirect
	github.com/golang/groupcache v0.0.0-20210331224755-41bb18bfe9da // indirect
	github.com/google/flatbuffers v24.3.25+incompatible // indirect
	github.com/klauspost/compress v1.17.9 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	go.opencensus.io v0.24.0 // indirect
	golang.org/x/net v0.28.0 // indirect
	golang.org/x/sys v0.28.0 // indirect
	golang.org/x/text v0.17.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20240701130421-f6361c86f094 // indirect
)
