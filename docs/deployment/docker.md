# Docker Compose Deployment

This guide covers deploying DecisionBox with Docker Compose for single-server environments.

## Quick Start

```bash
git clone https://github.com/decisionbox-io/decisionbox-platform.git
cd decisionbox-platform
docker compose up -d
```

Open **http://localhost:3000**.

## Services

| Service | Image | Port | Description |
|---------|-------|------|-------------|
| `mongodb` | `mongo:7.0` | 27017 | Database |
| `qdrant` | `qdrant/qdrant:v1.13.6` <!-- lint-allow: qdrant-image-tag --> | 6333/6334 | Vector search and schema-index storage |
| `api` | `decisionbox-api` | 8080 | REST API + agent spawning |
| `dashboard` | `decisionbox-dashboard` | 3000 | Web UI |

The API port (8080) does not need to be exposed publicly — the dashboard proxies all `/api/*` requests internally.

## Configuration

### Environment Variables

Key variables to configure (all have sensible defaults):

```yaml
services:
  api:
    environment:
      - MONGODB_URI=mongodb://mongodb:27017
      - MONGODB_DB=decisionbox
      - SECRET_PROVIDER=mongodb
      - SECRET_ENCRYPTION_KEY=${SECRET_ENCRYPTION_KEY}  # openssl rand -base64 32
      - RUNNER_MODE=subprocess
      - LOG_LEVEL=info
      - ENV=prod

  dashboard:
    environment:
      - API_URL=http://api:8080
```

See [Configuration Reference](../reference/configuration.md) for all variables.

### Agent Runner Mode

By default (`RUNNER_MODE=subprocess`) the API runs the agent as a child process inside the API container, so the agent binary shares the API image.

Set `RUNNER_MODE=docker` to have the API spawn each agent run as its own container from `AGENT_IMAGE` via the Docker engine instead. This is useful when the agent image must differ from the API image, or to mirror the production "API spawns the agent from an image" model on a single host without Kubernetes. The API talks to the engine over the mounted Docker socket and attaches each agent container to `AGENT_DOCKER_NETWORK` so it resolves `mongodb` / `qdrant` / warehouse hosts by service name:

```yaml
services:
  api:
    environment:
      - RUNNER_MODE=docker
      # Image the API launches per run. A public or locally-built image is
      # pulled if not present; a private-registry image must be pre-pulled
      # (the runner pulls with no registry credentials).
      - AGENT_IMAGE=ghcr.io/decisionbox-io/decisionbox-agent:latest
      # Compose network so the agent reaches mongodb/qdrant by name. Docker
      # names it "<project>_default"; the project defaults to the directory
      # name, so a clone of this repo uses "decisionbox-platform_default".
      # Confirm the exact name with `docker network ls`.
      - AGENT_DOCKER_NETWORK=decisionbox-platform_default
    volumes:
      # Mount the Docker socket so the API can spawn containers.
      - /var/run/docker.sock:/var/run/docker.sock
    # The API runs as non-root (UID/GID 1000), but /var/run/docker.sock is
    # usually root:docker (0660), so the container must join the host's docker
    # group or it cannot reach the socket (startup fails with "permission
    # denied"). Find the GID with: getent group docker | cut -d: -f3
    group_add:
      - "${DOCKER_GID:-999}"
```

> **Security:** mounting the Docker socket grants the API root-equivalent access to the host. Use `RUNNER_MODE=docker` only for local / single-host deployments; in production use the [Kubernetes runner](kubernetes.md), which spawns each agent as an isolated Job without a host socket.

### Generating Secrets

```bash
# Generate encryption key for MongoDB secret provider
export SECRET_ENCRYPTION_KEY=$(openssl rand -base64 32)
echo "SECRET_ENCRYPTION_KEY=$SECRET_ENCRYPTION_KEY" >> .env

# Docker Compose reads .env automatically
docker compose up -d
```

### Using Pre-built Images

Instead of building locally, use published images:

```yaml
services:
  api:
    image: ghcr.io/decisionbox-io/decisionbox-api:latest

  dashboard:
    image: ghcr.io/decisionbox-io/decisionbox-dashboard:latest
```

### Persistent Data

MongoDB and Qdrant data are stored in Docker volumes:

```yaml
volumes:
  mongodb_data:   # Survives container restarts
  qdrant_data:    # Schema-index + insight/recommendation vectors
```

To back up:
```bash
docker compose exec mongodb mongodump --out /dump
docker compose cp mongodb:/dump ./backup
```

## External MongoDB

To use an existing MongoDB instance (e.g., MongoDB Atlas):

```yaml
services:
  api:
    environment:
      - MONGODB_URI=mongodb+srv://user:pass@cluster.mongodb.net/?retryWrites=true
      - MONGODB_DB=decisionbox
```

Remove the `mongodb` service from docker-compose.yml and the `depends_on` reference.

## Reverse Proxy

Place nginx or Caddy in front of the dashboard for HTTPS:

```
                  Internet
                     │
              ┌──────┴──────┐
              │   nginx /   │
              │   Caddy     │  ← HTTPS termination
              │   :443      │
              └──────┬──────┘
                     │ HTTP
              ┌──────┴──────┐
              │  Dashboard  │
              │  :3000      │
              └─────────────┘
```

Example Caddy configuration:
```
docs.decisionbox.io {
    reverse_proxy dashboard:3000
}
```

## Updating

```bash
# Pull latest images
docker compose pull

# Restart with new images
docker compose up -d

# Or rebuild from source
docker compose up -d --build
```

## Logs

```bash
# All services
docker compose logs -f

# API only
docker compose logs -f api

# Agent runs (part of API logs)
docker compose logs -f api | grep "agent"
```

## Health Checks

```bash
# API
curl http://localhost:8080/health/ready

# Dashboard
curl http://localhost:3000/health
```

## Next Steps

- [Kubernetes (Helm)](kubernetes.md) — Production deployment on K8s
- [Terraform GCP](terraform-gcp.md) — Automated GKE cluster provisioning
- [Terraform AWS](terraform-aws.md) — Automated EKS cluster provisioning
- [Terraform Azure](terraform-azure.md) — Automated AKS cluster provisioning
- [Configuration Reference](../reference/configuration.md) — All environment variables
- [Production Considerations](production.md) — Security, scaling, monitoring
