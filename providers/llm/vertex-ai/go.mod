module github.com/decisionbox-io/decisionbox/providers/llm/vertex-ai

go 1.25.0

require (
	github.com/decisionbox-io/decisionbox/libs/go-common v0.0.0
	golang.org/x/oauth2 v0.36.0
)

require (
	cloud.google.com/go/compute/metadata v0.9.0 // indirect
	golang.org/x/sys v0.42.0 // indirect
)

replace github.com/decisionbox-io/decisionbox/libs/go-common => ../../../libs/go-common
