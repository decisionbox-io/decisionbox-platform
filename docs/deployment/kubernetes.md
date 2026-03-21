# Kubernetes Deployment

> **Version**: 0.1.0

Deploy DecisionBox on any Kubernetes cluster using Helm charts.

## Prerequisites

- Kubernetes cluster (GKE, EKS, AKS, self-managed, or any CNCF-conformant cluster)
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
  ├── manages secrets (AES-256 or cloud provider)
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
# Add the DecisionBox Helm repository
helm repo add decisionbox https://decisionbox-io.github.io/decisionbox-platform
helm repo update

# Create namespace
kubectl create namespace decisionbox

# Create API secrets (encryption key + MongoDB URI if using external MongoDB)
kubectl create secret generic decisionbox-api-secrets \
  --from-literal=SECRET_ENCRYPTION_KEY="$(openssl rand -base64 32)" \
  -n decisionbox

# Deploy API (with bundled MongoDB for quick start)
helm upgrade --install decisionbox-api decisionbox/decisionbox-api \
  --set "extraEnvFrom[0].secretRef.name=decisionbox-api-secrets" \
  -n decisionbox

# Deploy Dashboard
helm upgrade --install decisionbox-dashboard decisionbox/decisionbox-dashboard \
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

DecisionBox charts are published to a public Helm repository.
Source code is in `helm-charts/`.

```bash
helm repo add decisionbox https://decisionbox-io.github.io/decisionbox-platform
helm repo update
```

| Chart | Description | Default Port | Ingress |
|-------|-------------|-------------|---------|
| `decisionbox-api` | API service + optional MongoDB subchart | 8080 | Disabled (internal) |
| `decisionbox-dashboard` | Web dashboard | 3000 | Enabled |

## Injecting Secrets with extraEnvFrom

Sensitive values (MongoDB URI, encryption keys, API keys) should **never** be set via `--set env.KEY=VALUE` — that exposes them in shell history, `helm get values` output, and the Deployment spec.

Instead, store secrets in a K8s Secret and reference it with `extraEnvFrom`:

```bash
# Create a K8s Secret with all sensitive values
kubectl create secret generic decisionbox-api-secrets \
  --from-literal=SECRET_ENCRYPTION_KEY="$(openssl rand -base64 32)" \
  --from-literal=MONGODB_URI="mongodb+srv://user:pass@cluster.mongodb.net/decisionbox" \
  -n decisionbox

# Reference it in Helm
helm upgrade --install decisionbox-api decisionbox/decisionbox-api \
  --set "extraEnvFrom[0].secretRef.name=decisionbox-api-secrets" \
  -n decisionbox
```

The `extraEnvFrom` mechanism injects all keys from the K8s Secret as environment variables into the API pod. Use it for any value you don't want in version control or Helm output.

## Configuration

### External MongoDB (recommended for production)

Disable the bundled MongoDB subchart and provide your connection string via a K8s Secret:

```bash
# Create secret with MongoDB URI
kubectl create secret generic decisionbox-api-secrets \
  --from-literal=SECRET_ENCRYPTION_KEY="$(openssl rand -base64 32)" \
  --from-literal=MONGODB_URI="mongodb+srv://user:pass@cluster.mongodb.net/decisionbox?retryWrites=true" \
  -n decisionbox

# Deploy with external MongoDB
helm upgrade --install decisionbox-api decisionbox/decisionbox-api \
  --set mongodb.enabled=false \
  --set env.MONGODB_DB=decisionbox \
  --set "extraEnvFrom[0].secretRef.name=decisionbox-api-secrets" \
  -n decisionbox
```

### Secret Provider

By default, secrets are encrypted with AES-256 and stored in MongoDB. For production, use a cloud secret provider:

**GCP Secret Manager (with Workload Identity):**
```bash
helm upgrade --install decisionbox-api decisionbox/decisionbox-api \
  --set env.SECRET_PROVIDER=gcp \
  --set env.SECRET_GCP_PROJECT_ID=my-gcp-project \
  --set env.SECRET_NAMESPACE=decisionbox \
  --set "serviceAccountAnnotations.iam\.gke\.io/gcp-service-account=decisionbox-prod-api@my-gcp-project.iam.gserviceaccount.com" \
  --set "extraEnvFrom[0].secretRef.name=decisionbox-api-secrets" \
  -n decisionbox
```

The `serviceAccountAnnotations` binds the K8s service account to a GCP service account via [Workload Identity](https://cloud.google.com/kubernetes-engine/docs/how-to/workload-identity). See [Terraform GCP](terraform-gcp.md) for automated Workload Identity setup.

**AWS Secrets Manager (with IRSA or EKS Pod Identity):**
```bash
helm upgrade --install decisionbox-api decisionbox/decisionbox-api \
  --set env.SECRET_PROVIDER=aws \
  --set env.SECRET_AWS_REGION=us-east-1 \
  --set env.SECRET_NAMESPACE=decisionbox \
  --set "serviceAccountAnnotations.eks\.amazonaws\.com/role-arn=arn:aws:iam::123456789012:role/decisionbox-api" \
  --set "extraEnvFrom[0].secretRef.name=decisionbox-api-secrets" \
  -n decisionbox
```

The `serviceAccountAnnotations` binds the K8s service account to an IAM role via [IRSA](https://docs.aws.amazon.com/eks/latest/userguide/iam-roles-for-service-accounts.html) or [EKS Pod Identity](https://docs.aws.amazon.com/eks/latest/userguide/pod-identities.html).

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
# The API should remain ClusterIP. Do not enable API ingress.
ingress:
  enabled: false
```

### Dashboard API URL

The dashboard proxies `/api/*` requests to the API service. The default `API_URL` is `http://decisionbox-api-service:8080`, which assumes the API release name is `decisionbox-api`. If you use a different release name, update the dashboard's `env.API_URL`:

```bash
helm upgrade --install my-dashboard decisionbox/decisionbox-dashboard \
  --set env.API_URL="http://my-custom-api-service:8080" \
  -n decisionbox
```

### Using a Values File

For repeatable deployments, create a values override file:

```yaml
# values-prod.yaml (for decisionbox-api)

mongodb:
  enabled: false

env:
  MONGODB_DB: "decisionbox_prod"
  SECRET_PROVIDER: "gcp"
  SECRET_GCP_PROJECT_ID: "my-project"
  SECRET_NAMESPACE: "decisionbox"

extraEnvFrom:
  - secretRef:
      name: decisionbox-api-secrets

serviceAccountAnnotations:
  iam.gke.io/gcp-service-account: "decisionbox-prod-api@my-project.iam.gserviceaccount.com"

resources:
  requests:
    cpu: "250m"
    memory: "1Gi"
  limits:
    cpu: "2000m"
    memory: "4Gi"
```

Deploy with:
```bash
helm upgrade --install decisionbox-api decisionbox/decisionbox-api \
  -f values-prod.yaml -n decisionbox
```

Note: `MONGODB_URI` and `SECRET_ENCRYPTION_KEY` come from the `decisionbox-api-secrets` K8s Secret via `extraEnvFrom` — not from the values file.

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

| Service | Liveness | Readiness |
|---------|----------|-----------|
| API | `GET /health` — 15s initial, 30s period | `GET /health` — 5s initial, 10s period |
| Dashboard | `GET /health` — 15s initial, 15s period | `GET /health` — 5s initial, 10s period |

The API also exposes `GET /health/ready` which checks MongoDB connectivity. You can use this as the readiness probe path if you want readiness to depend on the database connection.

Run Helm tests to verify connectivity:

```bash
helm test decisionbox-api -n decisionbox
helm test decisionbox-dashboard -n decisionbox
```

## Updating

```bash
helm upgrade decisionbox-api decisionbox/decisionbox-api \
  -f values-prod.yaml -n decisionbox

helm upgrade decisionbox-dashboard decisionbox/decisionbox-dashboard \
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
- [Terraform AWS](terraform-aws.md) — Automated EKS cluster provisioning
- [Production Considerations](production.md) — Scaling, monitoring, backups
- [Configuration Reference](../reference/configuration.md) — All environment variables
