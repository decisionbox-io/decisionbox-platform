# Installation

DecisionBox can be installed in three ways, depending on your needs.

## Option 1: Docker Compose (Recommended)

The fastest way to get started. Runs all services in containers.

**Prerequisites:**
- Docker 24+ and Docker Compose v2+

```bash
git clone https://github.com/decisionbox-io/decisionbox-platform.git
cd decisionbox-platform
docker compose up -d
```

Open **http://localhost:3000**.

### Customizing the Stack

Edit `docker-compose.yml` to change ports, add volumes, or configure environment variables:

```yaml
services:
  api:
    environment:
      - SECRET_PROVIDER=mongodb
      - SECRET_ENCRYPTION_KEY=your-base64-key  # openssl rand -base64 32
      - LOG_LEVEL=info
```

See [Configuration Reference](../reference/configuration.md) for all available environment variables.

## Option 2: From Source (Development)

Run services directly on your machine. Best for development and contributing.

**Prerequisites:**
- Go 1.25+
- Node.js 20+
- MongoDB 7+ (or Docker for MongoDB only)
- Make

### Step 1: Clone and Build

```bash
git clone https://github.com/decisionbox-io/decisionbox-platform.git
cd decisionbox-platform

# Build Go binaries
make build

# Install dashboard dependencies
cd ui/dashboard && npm install && cd ../..
```

### Step 2: Start MongoDB

Use Docker for MongoDB only:

```bash
docker compose up -d mongodb
```

Or use an existing MongoDB instance — set `MONGODB_URI` accordingly.

### Step 3: Run Services

Open two terminals:

```bash
# Terminal 1: API
make dev-api

# Terminal 2: Dashboard
make dev-dashboard
```

The API runs on **http://localhost:8080** and the dashboard on **http://localhost:3000**.

### Step 4: Install Agent Binary

The API spawns the agent as a subprocess. The agent binary must be in your PATH:

```bash
make build-agent
sudo cp bin/decisionbox-agent /usr/local/bin/
```

## Option 3: Pre-built Binaries

Download pre-built binaries from [GitHub Releases](https://github.com/decisionbox-io/decisionbox-platform/releases).

```bash
# Download (example for Linux amd64)
curl -L https://github.com/decisionbox-io/decisionbox-platform/releases/download/v0.1.0/decisionbox-api-linux-amd64 -o decisionbox-api
curl -L https://github.com/decisionbox-io/decisionbox-platform/releases/download/v0.1.0/decisionbox-agent-linux-amd64 -o decisionbox-agent

chmod +x decisionbox-api decisionbox-agent
sudo mv decisionbox-agent /usr/local/bin/

# Run the API
MONGODB_URI=mongodb://localhost:27017 MONGODB_DB=decisionbox ./decisionbox-api
```

For the dashboard, you'll need to build from source or use the Docker image:

```bash
docker run -p 3000:3000 -e API_URL=http://host.docker.internal:8080 ghcr.io/decisionbox-io/decisionbox-dashboard:latest
```

## Docker Images

Three images are published to GitHub Container Registry:

| Image | Size | Description |
|-------|------|-------------|
| `ghcr.io/decisionbox-io/decisionbox-api` | ~84 MB | API server + agent binary + domain packs |
| `ghcr.io/decisionbox-io/decisionbox-agent` | ~47 MB | Standalone agent (for K8s Job mode) |
| `ghcr.io/decisionbox-io/decisionbox-dashboard` | ~213 MB | Next.js dashboard |

```bash
# Pull images
docker pull ghcr.io/decisionbox-io/decisionbox-api:latest
docker pull ghcr.io/decisionbox-io/decisionbox-dashboard:latest
```

## Verifying the Installation

Check that all services are healthy:

```bash
# API health
curl http://localhost:8080/health
# → {"status":"ok"}

# API readiness (checks MongoDB)
curl http://localhost:8080/health/ready
# → {"status":"ok","checks":{"mongodb":"ok"}}

# Dashboard health (checks API connectivity)
curl http://localhost:3000/health
# → {"status":"ok","services":{"api":{"status":"ok"}}}
```

## Next Steps

- [Quick Start](quickstart.md) — Create your first project and run a discovery
- [Your First Discovery](first-discovery.md) — Detailed walkthrough
- [Configuration Reference](../reference/configuration.md) — All environment variables
- [Development Setup](../contributing/development.md) — For contributors
