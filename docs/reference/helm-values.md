# Helm Values Reference

> **Version**: 0.1.0

Complete reference for all Helm chart values.
Charts are published to the DecisionBox Helm repository:

```bash
helm repo add decisionbox https://decisionbox-io.github.io/decisionbox-platform
helm repo update
```

Source code for the charts is in `helm-charts/`.

## decisionbox-api

### Image

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `replicaCount` | int | `1` | Number of API replicas |
| `image.repository` | string | `ghcr.io/decisionbox-io/decisionbox-api` | Container image |
| `image.tag` | string | `main` | Image tag (defaults to `appVersion` if not set) |
| `image.pullPolicy` | string | `Always` | Pull policy |
| `imagePullSecrets` | list | `[]` | Image pull secrets (set for private registries) |

### Deployment

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `namespace` | string | `decisionbox` | Kubernetes namespace |
| `containerPort` | int | `8080` | Container port |
| `serviceAccountName` | string | `decisionbox-api` | API service account name |
| `serviceAccountAnnotations` | map | `{}` | API SA annotations (e.g., Workload Identity) |
| `agentServiceAccount.name` | string | `decisionbox-agent` | Agent service account name (for K8s Jobs) |
| `agentServiceAccount.annotations` | map | `{}` | Agent SA annotations (e.g., Workload Identity for read-only access) |

### Environment Variables

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `env.ENV` | string | `prod` | Environment name |
| `env.LOG_LEVEL` | string | `warn` | Log level (debug, info, warn, error) |
| `env.MONGODB_URI` | string | — | MongoDB connection string (required if `mongodb.enabled=false`) |
| `env.MONGODB_DB` | string | `decisionbox` | MongoDB database name |
| `env.SECRET_PROVIDER` | string | `mongodb` | Secret provider: `mongodb`, `gcp`, or `aws` |
| `env.SECRET_NAMESPACE` | string | `decisionbox` | Secret name prefix |
| `env.SECRET_GCP_PROJECT_ID` | string | — | GCP project (when `SECRET_PROVIDER=gcp`) |
| `env.SECRET_AWS_REGION` | string | — | AWS region (when `SECRET_PROVIDER=aws`) |
| `env.RUNNER_MODE` | string | `kubernetes` | Agent runner: `kubernetes` or `subprocess` |
| `env.AGENT_IMAGE` | string | `ghcr.io/decisionbox-io/decisionbox-agent:latest` | Agent container image |
| `env.AGENT_NAMESPACE` | string | `decisionbox` | Namespace for agent Jobs |
| `env.AGENT_SERVICE_ACCOUNT` | string | `decisionbox-agent` | K8s service account for agent Jobs (Workload Identity) |
| `env.AGENT_JOB_TIMEOUT_HOURS` | string | `6` | Max time to watch agent Jobs |
| `extraEnv` | list | `[]` | Additional env vars as `{name, value}` pairs |
| `extraEnvFrom` | list | `[]` | Additional env sources (e.g., `secretRef`) |

### Resources

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `resources.requests.cpu` | string | `100m` | CPU request |
| `resources.requests.memory` | string | `512Mi` | Memory request |
| `resources.limits.cpu` | string | `1000m` | CPU limit |
| `resources.limits.memory` | string | `2Gi` | Memory limit |

### Service

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `service.type` | string | `ClusterIP` | Service type |
| `service.port` | int | `8080` | Service port |

### Ingress

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `ingress.enabled` | bool | `false` | Enable ingress (keep disabled — API is internal) |
| `ingress.host` | string | `""` | Hostname for host-based routing |
| `ingress.tlsSecretName` | string | `""` | TLS secret name |
| `ingress.pathType` | string | `Prefix` | Ingress path type |
| `ingress.path` | string | `/` | Ingress path |

### RBAC

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `rbac.enabled` | bool | `true` | Create Role + RoleBinding for agent Jobs |
| `rbac.roleName` | string | `agent-job-manager` | Role name |

### Probes

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `livenessProbe.path` | string | `/health` | Liveness endpoint |
| `livenessProbe.initialDelaySeconds` | int | `15` | Initial delay |
| `livenessProbe.periodSeconds` | int | `30` | Check interval |
| `readinessProbe.path` | string | `/health` | Readiness endpoint |
| `readinessProbe.initialDelaySeconds` | int | `5` | Initial delay |
| `readinessProbe.periodSeconds` | int | `10` | Check interval |

### Security Context

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `securityContext.runAsNonRoot` | bool | `true` | Require non-root |
| `securityContext.runAsUser` | int | `1000` | User ID |
| `securityContext.fsGroup` | int | `1000` | Filesystem group |
| `containerSecurityContext.readOnlyRootFilesystem` | bool | `true` | Read-only root FS |
| `containerSecurityContext.allowPrivilegeEscalation` | bool | `false` | No privilege escalation |
| `containerSecurityContext.capabilities.drop` | list | `[ALL]` | Drop all capabilities |

### MongoDB Subchart

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `mongodb.enabled` | bool | `true` | Deploy bundled MongoDB |
| `mongodb.architecture` | string | `standalone` | MongoDB architecture |
| `mongodb.auth.enabled` | bool | `false` | Enable MongoDB authentication |
| `mongodb.persistence.size` | string | `1Gi` | Persistent volume size |

When `mongodb.enabled=true`, the deployment includes an init container that waits for MongoDB to be ready. The MongoDB URI is auto-computed from the chart values.

For production, set `mongodb.enabled=false` and provide `env.MONGODB_URI` pointing to your MongoDB instance (Atlas or self-hosted).

---

## decisionbox-dashboard

### Image

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `replicaCount` | int | `1` | Number of dashboard replicas |
| `image.repository` | string | `ghcr.io/decisionbox-io/decisionbox-dashboard` | Container image |
| `image.tag` | string | `main` | Image tag |
| `image.pullPolicy` | string | `Always` | Pull policy |
| `imagePullSecrets` | list | `[]` | Image pull secrets (set for private registries) |

### Deployment

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `namespace` | string | `decisionbox` | Kubernetes namespace |
| `containerPort` | int | `3000` | Container port |
| `automountServiceAccountToken` | bool | `false` | Dashboard does not need K8s API access |

### Environment Variables

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `env.API_URL` | string | `http://decisionbox-api-service:8080` | API service URL (internal) |

The dashboard proxies `/api/*` requests to the API URL. This must point to the API's ClusterIP service.

### Resources

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `resources.requests.cpu` | string | `100m` | CPU request |
| `resources.requests.memory` | string | `128Mi` | Memory request |
| `resources.limits.cpu` | string | `500m` | CPU limit |
| `resources.limits.memory` | string | `512Mi` | Memory limit |

### Service

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `service.type` | string | `ClusterIP` | Service type |
| `service.port` | int | `3000` | Service port |

### Ingress

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `ingress.enabled` | bool | `true` | Enable ingress (dashboard is user-facing) |
| `ingress.host` | string | `""` | Hostname |
| `ingress.tlsSecretName` | string | `""` | TLS secret |
| `ingress.pathType` | string | `Prefix` | Path type |
| `ingress.path` | string | `/` | Path |

### Probes

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `livenessProbe.path` | string | `/health` | Liveness endpoint |
| `livenessProbe.initialDelaySeconds` | int | `15` | Initial delay |
| `livenessProbe.periodSeconds` | int | `15` | Check interval |
| `readinessProbe.path` | string | `/health` | Readiness endpoint |
| `readinessProbe.initialDelaySeconds` | int | `5` | Initial delay |
| `readinessProbe.periodSeconds` | int | `10` | Check interval |

### Security Context

Same as the API chart — non-root (UID 1000), read-only filesystem, no capabilities, seccomp RuntimeDefault. The dashboard mounts `/tmp` and `/app/.next/cache` as emptyDir volumes.

---

## Example: Production Values File

Sensitive values (`MONGODB_URI`, `SECRET_ENCRYPTION_KEY`) are stored in a K8s Secret and injected via `extraEnvFrom` — never in the values file.

```yaml
# values-prod.yaml (API)

mongodb:
  enabled: false

env:
  LOG_LEVEL: "warn"
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

Create the K8s Secret separately:

```bash
kubectl create secret generic decisionbox-api-secrets \
  --from-literal=SECRET_ENCRYPTION_KEY="$(openssl rand -base64 32)" \
  --from-literal=MONGODB_URI="mongodb+srv://user:pass@cluster.mongodb.net/decisionbox_prod" \
  -n decisionbox
```

## Next Steps

- [Kubernetes Deployment](../deployment/kubernetes.md) — Step-by-step deployment guide
- [Terraform GCP](../deployment/terraform-gcp.md) — Automated infrastructure provisioning
- [Configuration Reference](configuration.md) — All environment variables
