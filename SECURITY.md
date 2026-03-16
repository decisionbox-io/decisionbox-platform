# Security Policy

## Reporting a Vulnerability

If you discover a security issue in DecisionBox, please report it responsibly. **Do not open a public issue.**

### How to Report

Email **security@decisionbox.io** with:

1. Description of the issue
2. Steps to reproduce
3. Affected versions/components
4. Any potential impact assessment

### What to Expect

- **Acknowledgment** within 48 hours
- **Assessment** within 5 business days
- **Fix timeline** communicated after assessment
- **Credit** given in the security advisory (unless you prefer to remain anonymous)

## Supported Versions

| Version | Supported |
|---------|-----------|
| Latest release | Yes |
| Previous minor | Security fixes only |
| Older versions | No |

## Scope

The following are in scope:

- DecisionBox API (`services/api/`)
- DecisionBox Agent (`services/agent/`)
- DecisionBox Dashboard (`ui/dashboard/`)
- Helm charts (`helm-charts/`)
- Docker images (`ghcr.io/decisionbox-io/*`)
- Secret providers (`providers/secrets/`)

The following are out of scope:

- Third-party dependencies (report to the upstream project)
- Self-hosted instances with custom modifications
- Social engineering

## Security Best Practices for Self-Hosting

- Always set `SECRET_ENCRYPTION_KEY` for encrypting stored secrets (AES-256)
- Use external secret providers (GCP Secret Manager, AWS Secrets Manager) in production
- Run containers as non-root (Dockerfiles already configure this)
- Use network policies to restrict API access (API should not be publicly exposed -- only the dashboard)
- Keep images updated to the latest version
- Enable Kubernetes RBAC with least-privilege service accounts
