module github.com/decisionbox-io/decisionbox/providers/llm/openai

go 1.25.0

require github.com/decisionbox-io/decisionbox/libs/go-common v0.0.0

require (
	github.com/dlclark/regexp2 v1.10.0 // indirect
	github.com/google/uuid v1.3.0 // indirect
	github.com/pkoukk/tiktoken-go v0.1.8 // indirect
)

replace github.com/decisionbox-io/decisionbox/libs/go-common => ../../../libs/go-common
