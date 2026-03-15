# Helm Charts — Development Guide

## Charts Overview

### 1. decisionbox-api
- **Chart.yaml**: 1 Bitnami subchart (mongodb ~18.6.0)
- **values.yaml**: MongoDB disabled by default, in-cluster service addresses
- **Templates**: deployment, service, ingress, rbac, tests/test-health.yaml
- **Auto-wiring** (`_helpers.tpl`): mongodbURI
- **Env vars auto-overridden**: MONGODB_URI (when mongodb.enabled=true)

### 2. decisionbox-dashboard
- **Chart.yaml**: No dependencies (frontend connecting to decisionbox-api)
- **values.yaml**: API_HOSTNAME=decisionbox-api-service, API_PORT=8443
- **Templates**: deployment, service, ingress, tests/test-connection.yaml

## Infrastructure Dependencies

| Dependency | Type | Image | Status |
|-----------|------|-------|--------|
| MongoDB | Bitnami subchart | bitnami/mongodb:latest | Works on Docker Hub |

## Key Design Decisions
- MongoDB toggled via `mongodb.enabled` flag, disabled by default
- Auto-wiring: when mongodb is enabled, connection URI is auto-computed and overrides defaults
- No real secrets in repo — `.gitignore` blocks values-dev/prod/staging/local.yaml
- `.example` files provided for env-specific values
- Images default to GHCR (public registry)

## Security Hardening
- UID 1000 for all app containers
- seccompProfile: RuntimeDefault
- capabilities: drop ALL
- readOnlyRootFilesystem: true
- automountServiceAccountToken: false
- RBAC grants batch/jobs (create, get, list, delete) and core/pods (get, list) for K8s runner

## Files Structure
```
helm-charts/
├── .gitignore
├── CLAUDE.md
├── decisionbox-api/
│   ├── Chart.yaml
│   ├── values.yaml
│   ├── values-dev.yaml.example
│   ├── values-prod.yaml.example
│   ├── charts/
│   │   └── mongodb-18.6.1.tgz
│   └── templates/
│       ├── _helpers.tpl
│       ├── deployment.yaml
│       ├── service.yaml
│       ├── ingress.yaml
│       ├── rbac.yaml
│       └── tests/test-health.yaml
└── decisionbox-dashboard/
    ├── Chart.yaml
    ├── values.yaml
    ├── values-dev.yaml.example
    ├── values-prod.yaml.example
    └── templates/
        ├── _helpers.tpl
        ├── deployment.yaml
        ├── service.yaml
        ├── ingress.yaml
        └── tests/test-connection.yaml
```
