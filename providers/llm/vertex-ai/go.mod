module github.com/decisionbox-io/decisionbox/providers/llm/vertex-ai

go 1.25.0

require (
	github.com/decisionbox-io/decisionbox/libs/gcpcreds v0.0.0
	github.com/decisionbox-io/decisionbox/libs/go-common v0.0.0
	golang.org/x/oauth2 v0.36.0
)

require (
	cloud.google.com/go/auth v0.18.1 // indirect
	cloud.google.com/go/auth/oauth2adapt v0.2.8 // indirect
	cloud.google.com/go/compute/metadata v0.9.0 // indirect
	github.com/dlclark/regexp2 v1.10.0 // indirect
	github.com/google/s2a-go v0.1.9 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/googleapis/enterprise-certificate-proxy v0.3.11 // indirect
	github.com/googleapis/gax-go/v2 v2.16.0 // indirect
	github.com/pkoukk/tiktoken-go v0.1.8 // indirect
	golang.org/x/crypto v0.49.0 // indirect
	golang.org/x/net v0.51.0 // indirect
	golang.org/x/sys v0.42.0 // indirect
	golang.org/x/text v0.35.0 // indirect
	google.golang.org/api v0.265.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260128011058-8636f8732409 // indirect
	google.golang.org/grpc v1.78.0 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
)

replace (
	github.com/decisionbox-io/decisionbox/libs/gcpcreds => ../../../libs/gcpcreds
	github.com/decisionbox-io/decisionbox/libs/go-common => ../../../libs/go-common
)
