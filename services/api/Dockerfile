FROM golang:1.25-alpine AS builder

RUN apk add --no-cache git ca-certificates

WORKDIR /workspace

# Copy module files first for layer caching
COPY libs/go-common/go.mod libs/go-common/go.sum libs/go-common/
COPY libs/go-common/vectorstore/qdrant/go.mod libs/go-common/vectorstore/qdrant/go.sum libs/go-common/vectorstore/qdrant/
COPY providers/embedding/azure-openai/go.mod providers/embedding/azure-openai/
COPY providers/embedding/bedrock/go.mod providers/embedding/bedrock/go.sum providers/embedding/bedrock/
COPY providers/embedding/ollama/go.mod providers/embedding/ollama/
COPY providers/embedding/openai/go.mod providers/embedding/openai/
COPY providers/embedding/vertex-ai/go.mod providers/embedding/vertex-ai/go.sum providers/embedding/vertex-ai/
COPY providers/embedding/voyage/go.mod providers/embedding/voyage/
COPY providers/llm/claude/go.mod providers/llm/claude/
COPY providers/llm/openai/go.mod providers/llm/openai/
COPY providers/llm/ollama/go.mod providers/llm/ollama/go.sum providers/llm/ollama/
COPY providers/llm/vertex-ai/go.mod providers/llm/vertex-ai/go.sum providers/llm/vertex-ai/
COPY providers/llm/bedrock/go.mod providers/llm/bedrock/go.sum providers/llm/bedrock/
COPY providers/llm/azure-foundry/go.mod providers/llm/azure-foundry/
COPY providers/warehouse/bigquery/go.mod providers/warehouse/bigquery/go.sum providers/warehouse/bigquery/
COPY providers/warehouse/databricks/go.mod providers/warehouse/databricks/go.sum providers/warehouse/databricks/
COPY providers/warehouse/mssql/go.mod providers/warehouse/mssql/go.sum providers/warehouse/mssql/
COPY providers/warehouse/redshift/go.mod providers/warehouse/redshift/go.sum providers/warehouse/redshift/
COPY providers/warehouse/postgres/go.mod providers/warehouse/postgres/go.sum providers/warehouse/postgres/
COPY providers/warehouse/snowflake/go.mod providers/warehouse/snowflake/go.sum providers/warehouse/snowflake/
COPY providers/secrets/mongodb/go.mod providers/secrets/mongodb/go.sum providers/secrets/mongodb/
COPY providers/secrets/gcp/go.mod providers/secrets/gcp/go.sum providers/secrets/gcp/
COPY providers/secrets/aws/go.mod providers/secrets/aws/go.sum providers/secrets/aws/
COPY providers/secrets/azure/go.mod providers/secrets/azure/go.sum providers/secrets/azure/
COPY services/api/go.mod services/api/go.sum services/api/
COPY services/agent/go.mod services/agent/go.sum services/agent/
# Download deps
WORKDIR /workspace/services/api
RUN go mod download
WORKDIR /workspace/services/agent
RUN go mod download

# Copy all source
WORKDIR /workspace
COPY libs/ libs/
COPY providers/ providers/
COPY services/api/ services/api/
COPY services/agent/ services/agent/

# Build API binary
WORKDIR /workspace/services/api
RUN CGO_ENABLED=0 GOOS=linux go build -o /decisionbox-api .

# Build agent binary — required by API subprocess runner (RUNNER_MODE=subprocess),
# the default mode for docker-compose. In kubernetes mode the agent runs as a Job
# from its own image and this binary is unused.
WORKDIR /workspace/services/agent
RUN CGO_ENABLED=0 GOOS=linux go build -o /decisionbox-agent .

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata && \
    apk upgrade --no-cache && \
    addgroup -g 1000 -S app && adduser -u 1000 -S app -G app

WORKDIR /app

COPY --from=builder /decisionbox-api /usr/local/bin/decisionbox-api
COPY --from=builder /decisionbox-agent /usr/local/bin/decisionbox-agent

EXPOSE 8080
USER app
ENTRYPOINT ["decisionbox-api"]
