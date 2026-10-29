module github.com/sebbycorp/kubecon-adventures/arcade

go 1.27.3

require (
	github.com/a2aproject/a2a-go/v2 v2.6.0
	// kagent v1.0.0-alpha17. Its tags are repo-root, so the go/ module is pinned by commit.
	github.com/kagent-dev/kagent/go v0.0.0-20261021161150-8392184232e9
	google.golang.org/grpc v1.84.0
)

require (
	buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go v1.36.12-20260825204119-511051f7f437.2 // indirect
	github.com/agent-substrate/env v0.0.11-0.20260911201957-ab40c7bfb204 // indirect
	github.com/google/uuid v1.6.0 // indirect
	go.ate.dev/substrate v0.6.0-alpha3 // indirect
	golang.org/x/mod v0.42.0 // indirect
	golang.org/x/net v0.61.0 // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/text v0.43.0 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20261005182115-fad411399dd8 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20261005182115-fad411399dd8 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)

// Same Substrate module rewrite kagent's go.mod uses.
replace go.ate.dev/substrate => github.com/kagent-dev/substrate v0.6.0-alpha3
