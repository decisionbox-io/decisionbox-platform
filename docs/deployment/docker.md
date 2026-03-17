# Docker Compose Deployment

> **Version**: 0.1.0

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
      - DOMAIN_PACK_PATH=/app/domain-packs
      - RUNNER_MODE=subprocess
      - LOG_LEVEL=info
      - ENV=prod

  dashboard:
    environment:
      - API_URL=http://api:8080
```

See [Configuration Reference](../reference/configuration.md) for all variables.

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

MongoDB data is stored in a Docker volume:

```yaml
volumes:
  mongodb_data:   # Survives container restarts
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
- [Configuration Reference](../reference/configuration.md) — All environment variables
- [Production Considerations](production.md) — Security, scaling, monitoring
