module github.com/decisionbox-io/decisionbox/providers/llm/all

go 1.25.0

require (
	github.com/decisionbox-io/decisionbox/providers/llm/bedrock v0.0.0
	github.com/decisionbox-io/decisionbox/providers/llm/claude v0.0.0
	github.com/decisionbox-io/decisionbox/providers/llm/ollama v0.0.0
	github.com/decisionbox-io/decisionbox/providers/llm/openai v0.0.0
	github.com/decisionbox-io/decisionbox/providers/llm/vertex-ai v0.0.0
)

require (
	cloud.google.com/go/compute/metadata v0.9.0 // indirect
	github.com/aws/aws-sdk-go-v2 v1.41.3 // indirect
	github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream v1.7.6 // indirect
	github.com/aws/aws-sdk-go-v2/config v1.32.11 // indirect
	github.com/aws/aws-sdk-go-v2/credentials v1.19.11 // indirect
	github.com/aws/aws-sdk-go-v2/feature/ec2/imds v1.18.19 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.4.19 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.7.19 // indirect
	github.com/aws/aws-sdk-go-v2/internal/ini v1.8.5 // indirect
	github.com/aws/aws-sdk-go-v2/service/bedrockruntime v1.50.1 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.13.6 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/presigned-url v1.13.19 // indirect
	github.com/aws/aws-sdk-go-v2/service/signin v1.0.7 // indirect
	github.com/aws/aws-sdk-go-v2/service/sso v1.30.12 // indirect
	github.com/aws/aws-sdk-go-v2/service/ssooidc v1.35.16 // indirect
	github.com/aws/aws-sdk-go-v2/service/sts v1.41.8 // indirect
	github.com/aws/smithy-go v1.24.2 // indirect
	github.com/bahlo/generic-list-go v0.2.0 // indirect
	github.com/buger/jsonparser v1.1.2 // indirect
	github.com/decisionbox-io/decisionbox/libs/go-common v0.0.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/mailru/easyjson v0.7.7 // indirect
	github.com/ollama/ollama v0.18.1 // indirect
	github.com/wk8/go-ordered-map/v2 v2.1.8 // indirect
	golang.org/x/crypto v0.49.0 // indirect
	golang.org/x/oauth2 v0.36.0 // indirect
	golang.org/x/sys v0.42.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace (
	github.com/decisionbox-io/decisionbox/libs/go-common => ../../../libs/go-common
	github.com/decisionbox-io/decisionbox/providers/llm/bedrock => ../bedrock
	github.com/decisionbox-io/decisionbox/providers/llm/claude => ../claude
	github.com/decisionbox-io/decisionbox/providers/llm/ollama => ../ollama
	github.com/decisionbox-io/decisionbox/providers/llm/openai => ../openai
	github.com/decisionbox-io/decisionbox/providers/llm/vertex-ai => ../vertex-ai
)
