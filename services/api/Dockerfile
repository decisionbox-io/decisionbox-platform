FROM golang:1.25-alpine AS builder

RUN apk add --no-cache git ca-certificates

WORKDIR /workspace

# Copy go.work and module files first for layer caching
COPY go.work go.work
COPY libs/go-common/go.mod libs/go-common/go.sum libs/go-common/
# Aggregator packages
COPY providers/llm/all/go.mod providers/llm/all/go.sum providers/llm/all/
COPY providers/secrets/all/go.mod providers/secrets/all/go.sum providers/secrets/all/
COPY providers/warehouse/all/go.mod providers/warehouse/all/go.sum providers/warehouse/all/
COPY domain-packs/all/go.mod domain-packs/all/go.sum domain-packs/all/
# LLM providers
COPY providers/llm/claude/go.mod providers/llm/claude/
COPY providers/llm/openai/go.mod providers/llm/openai/
COPY providers/llm/ollama/go.mod providers/llm/ollama/go.sum providers/llm/ollama/
COPY providers/llm/vertex-ai/go.mod providers/llm/vertex-ai/go.sum providers/llm/vertex-ai/
COPY providers/llm/bedrock/go.mod providers/llm/bedrock/go.sum providers/llm/bedrock/
# Warehouse providers
COPY providers/warehouse/bigquery/go.mod providers/warehouse/bigquery/go.sum providers/warehouse/bigquery/
COPY providers/warehouse/redshift/go.mod providers/warehouse/redshift/go.sum providers/warehouse/redshift/
# Secret providers
COPY providers/secrets/mongodb/go.mod providers/secrets/mongodb/go.sum providers/secrets/mongodb/
COPY providers/secrets/gcp/go.mod providers/secrets/gcp/go.sum providers/secrets/gcp/
COPY providers/secrets/aws/go.mod providers/secrets/aws/go.sum providers/secrets/aws/
# Domain packs
COPY domain-packs/gaming/go/go.mod domain-packs/gaming/go/
COPY domain-packs/social/go/go.mod domain-packs/social/go/
# Service
COPY services/api/go.mod services/api/go.sum services/api/
# Download deps
WORKDIR /workspace/services/api
RUN go mod download

# Copy all source
WORKDIR /workspace
COPY libs/ libs/
COPY providers/ providers/
COPY domain-packs/ domain-packs/
COPY services/api/ services/api/

# Build API binary
WORKDIR /workspace/services/api
RUN CGO_ENABLED=0 GOOS=linux go build -o /decisionbox-api .

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata && \
    apk upgrade --no-cache && \
    addgroup -g 1000 -S app && adduser -u 1000 -S app -G app

WORKDIR /app

COPY --from=builder /decisionbox-api /usr/local/bin/decisionbox-api

# Include domain pack prompts and profiles (read at runtime, not embedded)
COPY --from=builder /workspace/domain-packs/ /app/domain-packs/

ENV DOMAIN_PACK_PATH=/app/domain-packs

EXPOSE 8080
USER app
ENTRYPOINT ["decisionbox-api"]
