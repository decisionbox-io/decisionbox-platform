# Kubernetes Deployment

> **Version**: 0.1.0

Deploy DecisionBox on any Kubernetes cluster using Helm charts.

## Prerequisites

- Kubernetes cluster (GKE, EKS, self-managed, or any CNCF-conformant cluster)
- [Helm 3.7+](https://helm.sh/docs/intro/install/)
- `kubectl` configured for your cluster
- MongoDB instance (Atlas, self-hosted, or the bundled Bitnami subchart)

## Architecture

```
Ingress
  └── Dashboard (Next.js, port 3000)
        └── proxies /api/* to API

API Service (Go, port 8080, ClusterIP)
  ├── spawns Agent as K8s Jobs
  ├── connects to MongoDB
  └── reads domain pack prompts from /app/domain-packs/

Agent Jobs (Go, spawned per discovery run)
  ├── connects to MongoDB
  ├── calls LLM provider
  └── queries data warehouse

MongoDB (standalone or Atlas)
```

The API is internal only (`ClusterIP`) — never exposed to the internet. The dashboard is the only public-facing service and proxies all API requests server-side.

## Quick Start

```bash
# Clone the repository
git clone https://github.com/decisionbox-io/decisionbox-platform.git
cd decisionbox-platform

# Create namespace
kubectl create namespace decisionbox

# Generate encryption key for secrets
kubectl create secret generic decisionbox-api-secrets \
  --from-literal=SECRET_ENCRYPTION_KEY="$(openssl rand -base64 32)" \
  -n decisionbox

# Deploy API (with bundled MongoDB)
helm upgrade --install decisionbox-api ./helm-charts/decisionbox-api \
  -n decisionbox

# Deploy Dashboard
helm upgrade --install decisionbox-dashboard ./helm-charts/decisionbox-dashboard \
  -n decisionbox

# Verify
kubectl get pods -n decisionbox
helm test decisionbox-api -n decisionbox
```

The dashboard is accessible via the ingress (enabled by default). Check the external IP:

```bash
kubectl get ingress -n decisionbox
```

## Charts

DecisionBox ships two Helm charts:

| Chart | Description | Default Port | Ingress |
|-------|-------------|-------------|---------|
| `decisionbox-api` | API service + optional MongoDB subchart | 8080 | Disabled (internal) |
| `decisionbox-dashboard` | Web dashboard | 3000 | Enabled |

## Configuration

### External MongoDB (recommended for production)

Disable the bundled MongoDB subchart and provide your own connection string:

```bash
helm upgrade --install decisionbox-api ./helm-charts/decisionbox-api \
  --set mongodb.enabled=false \
  --set env.MONGODB_URI="mongodb+srv://user:pass@cluster.mongodb.net/decisionbox?retryWrites=true" \
  --set env.MONGODB_DB=decisionbox \
  -n decisionbox
```

### Secret Provider

By default, secrets are encrypted with AES-256 and stored in MongoDB. For production, use a cloud secret provider:

**GCP Secret Manager:**
```bash
helm upgrade --install decisionbox-api ./helm-charts/decisionbox-api \
  --set env.SECRET_PROVIDER=gcp \
  --set env.SECRET_GCP_PROJECT_ID=my-gcp-project \
  --set env.SECRET_NAMESPACE=decisionbox \
  -n decisionbox
```

Requires Workload Identity configured (see [Terraform GCP](terraform-gcp.md) for automated setup).

**AWS Secrets Manager:**
```bash
helm upgrade --install decisionbox-api ./helm-charts/decisionbox-api \
  --set env.SECRET_PROVIDER=aws \
  --set env.SECRET_AWS_REGION=us-east-1 \
  --set env.SECRET_NAMESPACE=decisionbox \
  -n decisionbox
```

Requires IAM roles for service accounts (IRSA) on EKS.

### Agent Configuration

The API spawns agent processes as K8s Jobs. Configure via Helm values:

```yaml
env:
  RUNNER_MODE: "kubernetes"
  AGENT_IMAGE: "ghcr.io/decisionbox-io/decisionbox-agent:latest"
  AGENT_NAMESPACE: "decisionbox"
  AGENT_JOB_TIMEOUT_HOURS: "6"
```

The chart includes RBAC rules that grant the API service account permission to create and manage Jobs in its namespace.

### Ingress

**Dashboard** (enabled by default):
```yaml
# helm-charts/decisionbox-dashboard/values.yaml
ingress:
  enabled: true
  host: "dashboard.example.com"   # optional: host-based routing
  tlsSecretName: "dashboard-tls"  # optional: TLS
  pathType: Prefix
  path: /
```

**API** (disabled by default — keep it internal):
```yaml
# Only enable if you need direct API access (not recommended)
ingress:
  enabled: false
```

### Using a Values File

For repeatable deployments, create a values override file:

```yaml
# values-prod.yaml
replicaCount: 2

env:
  ENV: "prod"
  MONGODB_URI: "mongodb+srv://user:pass@cluster.mongodb.net/decisionbox_prod"
  MONGODB_DB: "decisionbox_prod"
  SECRET_PROVIDER: "gcp"
  SECRET_GCP_PROJECT_ID: "my-project"
  SECRET_NAMESPACE: "decisionbox"

mongodb:
  enabled: false

resources:
  requests:
    cpu: "250m"
    memory: "1Gi"
  limits:
    cpu: "2000m"
    memory: "4Gi"

ingress:
  host: "dashboard.example.com"
  tlsSecretName: "dashboard-tls"
```

Deploy with:
```bash
helm upgrade --install decisionbox-api ./helm-charts/decisionbox-api \
  -f values-prod.yaml -n decisionbox
```

## Security

All pods run with hardened security contexts:

- Non-root user (UID 1000)
- Read-only root filesystem (`/tmp` mounted as emptyDir)
- No Linux capabilities (`drop: ALL`)
- Seccomp profile: `RuntimeDefault`
- Pod anti-affinity (distributes replicas across nodes)

The API service account has scoped RBAC permissions to manage agent Jobs:

| Resource | Verbs |
|----------|-------|
| `batch/jobs` | create, get, list, delete |
| `core/pods` | get, list |

These permissions are namespace-scoped (Role, not ClusterRole).

## Health Checks

Both charts configure liveness and readiness probes:

| Service | Endpoint | Liveness | Readiness |
|---------|----------|----------|-----------|
| API | `/health` | 15s initial, 30s period | 5s initial, 10s period |
| Dashboard | `/health` | 15s initial, 15s period | 5s initial, 10s period |

Run Helm tests to verify connectivity:

```bash
helm test decisionbox-api -n decisionbox
helm test decisionbox-dashboard -n decisionbox
```

## Updating

```bash
# Pull latest images and upgrade
helm upgrade decisionbox-api ./helm-charts/decisionbox-api \
  -f values-prod.yaml -n decisionbox

helm upgrade decisionbox-dashboard ./helm-charts/decisionbox-dashboard \
  -n decisionbox
```

The API re-creates MongoDB indexes on startup (idempotent). No database migrations needed.

## Uninstalling

```bash
helm uninstall decisionbox-dashboard -n decisionbox
helm uninstall decisionbox-api -n decisionbox
kubectl delete namespace decisionbox
```

## Next Steps

- [Helm Values Reference](../reference/helm-values.md) — Complete values.yaml documentation
- [Terraform GCP](terraform-gcp.md) — Automated GKE cluster provisioning
- [Production Considerations](production.md) — Scaling, monitoring, backups
- [Configuration Reference](../reference/configuration.md) — All environment variables
