FROM golang:1.25-alpine AS builder

RUN apk add --no-cache git ca-certificates

WORKDIR /workspace

# Copy module files first for layer caching
COPY libs/go-common/go.mod libs/go-common/go.sum libs/go-common/
COPY providers/llm/claude/go.mod providers/llm/claude/
COPY providers/llm/openai/go.mod providers/llm/openai/
COPY providers/llm/ollama/go.mod providers/llm/ollama/go.sum providers/llm/ollama/
COPY providers/llm/vertex-ai/go.mod providers/llm/vertex-ai/
COPY providers/llm/bedrock/go.mod providers/llm/bedrock/
COPY providers/warehouse/bigquery/go.mod providers/warehouse/bigquery/go.sum providers/warehouse/bigquery/
COPY domain-packs/gaming/go/go.mod domain-packs/gaming/go/
COPY services/api/go.mod services/api/go.sum services/api/
COPY services/agent/go.mod services/agent/go.sum services/agent/

# Download deps for both services
WORKDIR /workspace/services/api
RUN go mod download
WORKDIR /workspace/services/agent
RUN go mod download

# Copy all source
WORKDIR /workspace
COPY libs/ libs/
COPY providers/ providers/
COPY domain-packs/ domain-packs/
COPY services/api/ services/api/
COPY services/agent/ services/agent/

# Build both binaries
WORKDIR /workspace/services/api
RUN CGO_ENABLED=0 GOOS=linux go build -o /decisionbox-api .

WORKDIR /workspace/services/agent
RUN CGO_ENABLED=0 GOOS=linux go build -o /decisionbox-agent .

FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata && \
    addgroup -g 1000 -S app && adduser -u 1000 -S app -G app

# Include both binaries — API spawns agent as subprocess
COPY --from=builder /decisionbox-api /usr/local/bin/decisionbox-api
COPY --from=builder /decisionbox-agent /usr/local/bin/decisionbox-agent

EXPOSE 8080
USER app
ENTRYPOINT ["decisionbox-api"]
