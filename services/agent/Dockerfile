FROM golang:1.25-alpine AS builder

RUN apk add --no-cache git ca-certificates

WORKDIR /workspace

# Copy module files first for layer caching
COPY libs/go-common/go.mod libs/go-common/go.sum libs/go-common/
COPY providers/llm/claude/go.mod providers/llm/claude/
COPY providers/llm/openai/go.mod providers/llm/openai/
COPY providers/llm/ollama/go.mod providers/llm/ollama/go.sum providers/llm/ollama/
COPY providers/llm/vertex-ai/go.mod providers/llm/vertex-ai/go.sum providers/llm/vertex-ai/
COPY providers/llm/bedrock/go.mod providers/llm/bedrock/go.sum providers/llm/bedrock/
COPY providers/llm/azure-foundry/go.mod providers/llm/azure-foundry/
COPY providers/warehouse/bigquery/go.mod providers/warehouse/bigquery/go.sum providers/warehouse/bigquery/
COPY providers/warehouse/databricks/go.mod providers/warehouse/databricks/go.sum providers/warehouse/databricks/
COPY providers/warehouse/redshift/go.mod providers/warehouse/redshift/go.sum providers/warehouse/redshift/
COPY providers/warehouse/postgres/go.mod providers/warehouse/postgres/go.sum providers/warehouse/postgres/
COPY providers/warehouse/snowflake/go.mod providers/warehouse/snowflake/go.sum providers/warehouse/snowflake/
COPY providers/secrets/mongodb/go.mod providers/secrets/mongodb/go.sum providers/secrets/mongodb/
COPY providers/secrets/gcp/go.mod providers/secrets/gcp/go.sum providers/secrets/gcp/
COPY providers/secrets/aws/go.mod providers/secrets/aws/go.sum providers/secrets/aws/
COPY providers/secrets/azure/go.mod providers/secrets/azure/go.sum providers/secrets/azure/
COPY services/agent/go.mod services/agent/go.sum services/agent/

WORKDIR /workspace/services/agent
RUN go mod download

# Copy source
WORKDIR /workspace
COPY libs/ libs/
COPY providers/ providers/
COPY services/agent/ services/agent/

WORKDIR /workspace/services/agent
RUN CGO_ENABLED=0 GOOS=linux go build -o /decisionbox-agent .

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata && \
    apk upgrade --no-cache && \
    addgroup -g 1000 -S app && adduser -u 1000 -S app -G app

WORKDIR /app

COPY --from=builder /decisionbox-agent /usr/local/bin/decisionbox-agent

USER app
ENTRYPOINT ["decisionbox-agent"]
