module github.com/decisionbox-io/decisionbox/providers/llm/vertex-ai

go 1.26.8

require (
	github.com/decisionbox-io/decisionbox/libs/gcpcreds v0.0.0
	github.com/decisionbox-io/decisionbox/libs/go-common v0.0.0
	golang.org/x/oauth2 v0.37.0
)

require (
	cloud.google.com/go/auth v0.18.2 // indirect
	cloud.google.com/go/auth/oauth2adapt v0.2.8 // indirect
	cloud.google.com/go/compute/metadata v0.9.0 // indirect
	github.com/dlclark/regexp2 v1.10.0 // indirect
	github.com/google/s2a-go v0.1.9 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/googleapis/enterprise-certificate-proxy v0.3.11 // indirect
	github.com/googleapis/gax-go/v2 v2.17.0 // indirect
	github.com/pkoukk/tiktoken-go v0.1.8 // indirect
	github.com/stretchr/testify v1.12.1 // indirect
	go.opentelemetry.io/otel/metric v1.46.0 // indirect
	go.opentelemetry.io/otel/sdk v1.46.0 // indirect
	go.opentelemetry.io/otel/trace v1.46.0 // indirect
	golang.org/x/crypto v0.56.0 // indirect
	golang.org/x/net v0.58.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/text v0.41.0 // indirect
	google.golang.org/api v0.265.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260526163538-3dc84a4a5aaa // indirect
	google.golang.org/grpc v1.83.2 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)

replace (
	github.com/decisionbox-io/decisionbox/libs/gcpcreds => ../../../libs/gcpcreds
	github.com/decisionbox-io/decisionbox/libs/go-common => ../../../libs/go-common
)
