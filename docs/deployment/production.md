# Production Considerations

> **Version**: 0.1.0

Recommendations for running DecisionBox in production.

## Security

### Secret Encryption

Always set `SECRET_ENCRYPTION_KEY` in production:

```bash
export SECRET_ENCRYPTION_KEY=$(openssl rand -base64 32)
```

Without it, LLM API keys are stored in plaintext in MongoDB.

### API Access

The API has **no authentication** in v0.1.0. It should not be exposed to the internet:

- **Docker Compose**: Only expose the dashboard port (3000). The API port (8080) should not be mapped to the host or should be restricted to the Docker network.
- **Kubernetes**: The API service should be `ClusterIP` (internal only). Only the dashboard needs an ingress.

The dashboard proxies all `/api/*` requests to the API server-side. Users never talk to the API directly.

### Cloud Secret Providers

For production, use a cloud secret provider instead of MongoDB:

```bash
# GCP
SECRET_PROVIDER=gcp
SECRET_GCP_PROJECT_ID=my-project

# AWS
SECRET_PROVIDER=aws
SECRET_AWS_REGION=us-east-1

# Azure
SECRET_PROVIDER=azure
SECRET_AZURE_VAULT_URL=https://my-vault.vault.azure.net/
```

Cloud providers handle encryption, access control, audit logging, and key rotation.

### Network

- MongoDB should not be accessible from the internet
- Use MongoDB authentication (username/password or x509)
- Enable TLS for MongoDB connections in production

## Scaling

### Current Limitations

- **Single agent per discovery**: Each discovery run spawns one agent process. Parallel projects work, but parallel runs within a project are blocked (409 Conflict).
- **No horizontal API scaling**: The API stores run state in MongoDB. Multiple API replicas work for reads but may have race conditions for run management. Use a single API replica for now.
- **Dashboard is stateless**: Can be scaled horizontally with multiple replicas behind a load balancer.

### Resource Sizing

| Component | Small (dev) | Medium (production) | Large (heavy use) |
|-----------|------------|--------------------|--------------------|
| API | 256Mi / 0.5 CPU | 512Mi / 1 CPU | 1Gi / 2 CPU |
| Agent | 256Mi / 0.5 CPU | 1Gi / 2 CPU | 2Gi / 4 CPU |
| Dashboard | 128Mi / 0.25 CPU | 256Mi / 0.5 CPU | 512Mi / 1 CPU |
| MongoDB | 512Mi / 1 CPU | 2Gi / 2 CPU | 8Gi / 4 CPU |

Agent resource usage depends on:
- Number of exploration steps
- Size of warehouse query results
- LLM response sizes

### MongoDB

For production MongoDB:
- Use MongoDB Atlas (managed) or a MongoDB operator on Kubernetes
- Enable replica set for availability
- Set appropriate WiredTiger cache size
- Index maintenance is automatic (API creates indexes on startup)

## Monitoring

### Health Endpoints

| Endpoint | Purpose | Frequency |
|----------|---------|-----------|
| `GET /health` | Liveness (API process alive) | K8s: every 30s (API), 15s (Dashboard) |
| `GET /health/ready` | Readiness (MongoDB connected) | K8s: every 10s |
| `GET /health` on :3000 | Dashboard liveness | K8s: every 15s |

### Logs

Both API and agent write structured JSON logs to stderr in production mode (`ENV=prod`):

```json
{"level":"info","ts":"2026-03-14T10:30:00.000Z","msg":"Discovery completed","service":"decisionbox-agent","project_id":"507f...","insights":7,"duration":"5m23s"}
```

Collect with any log aggregator (Loki, CloudWatch, Cloud Logging, Datadog).

### Key Metrics to Watch

| Metric | Source | Alert On |
|--------|--------|----------|
| Discovery run duration | Agent logs | > 30 minutes |
| Discovery failures | `discovery_runs.status = "failed"` | Any failure |
| LLM timeouts | Agent logs (ERROR level) | Repeated timeouts |
| Warehouse query errors | Agent logs | > 10% failure rate |
| MongoDB connection errors | API logs | Any connection error |

## Backup and Recovery

### MongoDB

```bash
# Backup
mongodump --uri="$MONGODB_URI" --db=decisionbox --out=./backup

# Restore
mongorestore --uri="$MONGODB_URI" --db=decisionbox ./backup/decisionbox
```

### What to Back Up

| Collection | Priority | Size |
|-----------|----------|------|
| `projects` | Critical | Small |
| `secrets` | Critical | Small |
| `discoveries` | Important | Large (grows over time) |
| `feedback` | Important | Small |
| `discovery_runs` | Low (ephemeral) | Medium |
| `discovery_debug_logs` | Low (TTL: 30 days) | Large |
| `pricing` | Low (auto-seeded) | Tiny |

## Maintenance

### Cleaning Up Old Data

Discovery debug logs have a 30-day TTL index and are cleaned up automatically by MongoDB.

Discovery results accumulate indefinitely. Consider periodically archiving or deleting old discoveries:

```javascript
// Delete discoveries older than 90 days
db.discoveries.deleteMany({
  created_at: { $lt: new Date(Date.now() - 90*24*60*60*1000) }
})
```

### Updating

1. Pull new images (or rebuild from source)
2. Stop services
3. Start services — the API re-creates indexes automatically (idempotent)
4. No database migrations needed — MongoDB is schema-flexible

## Next Steps

- [Docker Compose](docker.md) — Single-server deployment
- [Kubernetes (Helm)](kubernetes.md) — Production deployment on K8s
- [Terraform GCP](terraform-gcp.md) — Automated GKE cluster provisioning
- [Helm Values Reference](../reference/helm-values.md) — Complete chart configuration
- [Configuration Reference](../reference/configuration.md) — All environment variables
