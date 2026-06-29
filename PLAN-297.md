# Plan: Push Agent Image to Both Dev and Prod ECR Accounts

## Problem Restatement

The Docker publish workflow (`.github/workflows/docker-publish.yml`) currently pushes the `decisionbox-agent` image to a single AWS ECR registry (the dev account, `435998721348`). The cloud platform runs separate dev and prod EKS clusters in different AWS accounts, and the prod cluster (`843802784535`) needs to pull the agent image from its own same-account ECR — cross-account pulls would require additional IAM trust policies and are slower. The workflow must push to **both** ECR accounts after the GHCR push.

## Proposed Approach

Add a second set of AWS credential-assume + ECR-push steps immediately after the existing dev-ECR push block, targeting the prod account. The pattern is identical — OIDC role assumption via `aws-actions/configure-aws-credentials@v4`, then `docker buildx imagetools create` to copy the multi-arch manifest from GHCR to the prod ECR registry. This mirrors the existing dev-ECR steps exactly, just with the prod secrets.

**Why `docker buildx imagetools create` (not `docker tag` + `docker push`):** The workflow already builds and pushes multi-arch manifests (amd64+arm64) to GHCR. `imagetools create` copies the manifest list cross-registry without re-pulling layers locally — it's the cheapest way to mirror a multi-arch image and is already proven in the existing dev-ECR block.

## Files to Change

| File | Change |
|------|--------|
| `.github/workflows/docker-publish.yml` | Add prod ECR credential + login + push steps (3 new steps) |
| `CHANGELOG.md` | Entry under `[Unreleased]` |
| `docs/deployment/kubernetes.md` | Update ECR comment to note both dev/prod accounts |
| `docs/reference/configuration.md` | Update ECR mirror mentions to note both accounts |

## Step-by-Step Implementation

### Phase 1: Workflow Changes (`.github/workflows/docker-publish.yml`)

After the existing "Push to ECR" step (line ~355–365), add three new steps:

1. **Configure AWS credentials (prod)** — Same `if` conditional as the dev steps (`(matrix.changed == 'true' || github.ref_type == 'tag' || github.event_name == 'workflow_dispatch') && matrix.service == 'decisionbox-agent'`). Uses `secrets.AWS_DEPLOY_ROLE_ARN_PROD` for the role assumption.

2. **Login to Amazon ECR (prod)** — `aws-actions/amazon-ecr-login@v2` with `registries: ${{ secrets.AWS_ACCOUNT_ID_PROD }}`.

3. **Push to ECR (prod)** — Same `docker buildx imagetools create` loop as the dev step, but targeting `${{ secrets.AWS_ACCOUNT_ID_PROD }}.dkr.ecr.us-east-1.amazonaws.com/decisionbox-agent:<tag>`.

The step names will be suffixed with `(prod)` to distinguish them in the workflow UI (the existing dev steps will be renamed with `(dev)` suffixes for clarity).

### Phase 2: Documentation Updates

1. **`CHANGELOG.md`** — Add an entry under `[Unreleased] > Added` noting that CI now pushes the agent image to both dev and prod ECR accounts.

2. **`docs/deployment/kubernetes.md`** (line 183–184) — The existing ECR comment is generic (`<account-id>.dkr.ecr...`); no change needed since it already uses a placeholder. Confirmed adequate.

3. **`docs/reference/configuration.md`** (lines 179, 193) — Both `AGENT_IMAGE` entries already use the generic `<account-id>` placeholder. No substantive change needed here either — the text correctly describes that any same-account ECR works. Confirmed adequate.

**Conclusion on docs:** The existing documentation uses generic `<account-id>` placeholders and doesn't name a specific account. Since the prod/dev distinction is an operational detail of the cloud platform (not user-facing configuration), the docs are already correct. Only the `CHANGELOG` needs an update.

### Phase 3: Rename Existing Dev Steps for Clarity

Rename the existing three ECR steps:
- "Configure AWS credentials" → "Configure AWS credentials (dev)"
- "Login to Amazon ECR" → "Login to Amazon ECR (dev)"
- "Push to ECR" → "Push to ECR (dev)"

This makes the workflow run UI unambiguous about which account each step targets.

## Required Secrets

The following secrets must be pre-configured in the repository settings (they are not committed to code):

| Secret | Value | Status |
|--------|-------|--------|
| `AWS_DEPLOY_ROLE_ARN` | `arn:aws:iam::435998721348:role/dbx-cloud-dev-github-actions` | Already set |
| `AWS_ACCOUNT_ID` | `435998721348` | Already set |
| `AWS_DEPLOY_ROLE_ARN_PROD` | `arn:aws:iam::843802784535:role/dbx-cloud-prod-github-actions` | **Must be set** |
| `AWS_ACCOUNT_ID_PROD` | `843802784535` | **Must be set** |

The OIDC trust policy on the prod role must allow `token.actions.githubusercontent.com` for this repository (same configuration as the dev role). This is an infra prerequisite, not a code change.

## Conditional Logic

All six ECR steps (3 dev + 3 prod) share the same `if` conditional:
```yaml
if: (matrix.changed == 'true' || github.ref_type == 'tag' || github.event_name == 'workflow_dispatch') && matrix.service == 'decisionbox-agent'
```

This ensures:
- Only the agent image goes to ECR (API/dashboard stay GHCR-only)
- Unchanged agent code on a non-tag push skips the ECR push
- Tags and manual dispatches always push (release + hotfix flows)

## Failure Isolation

The prod ECR steps run **after** the dev ECR steps. If the prod push fails:
- GHCR images are already published (the multi-arch push completes before any ECR steps)
- Dev ECR images are already published (the dev steps complete before prod starts)
- Only prod ECR is affected — acceptable partial state for a single push failure

This mirrors the existing design decision where ECR is downstream of GHCR.

## Test Strategy

### Verification approach (CI/CD workflow — no unit tests)

1. **YAML validation** — Parse the workflow file locally (`python -c "import yaml; yaml.safe_load(open(...))"` or `actionlint` if available) to catch syntax errors.

2. **Dry-run on PR** — The workflow's `if` conditions skip the push job on PRs (`github.event_name != 'pull_request'`). The steps will appear in the workflow definition but won't execute until merge. This is the existing gating pattern.

3. **Post-merge validation** — After merge to `main` or a tag push:
   - Confirm the workflow run shows all 6 ECR steps (3 dev + 3 prod) as green for the agent matrix entry
   - Confirm they're skipped for api/dashboard entries
   - Run `docker manifest inspect 843802784535.dkr.ecr.us-east-1.amazonaws.com/decisionbox-agent:<tag>` to confirm multi-arch (amd64+arm64) on prod ECR

### Edge cases

- **Missing `AWS_DEPLOY_ROLE_ARN_PROD` secret** — The `configure-aws-credentials` step will fail with a clear error message ("Credentials could not be loaded"). The dev push and GHCR push are unaffected (they complete before the prod steps).
- **Missing `AWS_ACCOUNT_ID_PROD` secret** — The ECR login step will fail (empty registry string). Same isolation as above.
- **Prod OIDC role not trusted** — The `configure-aws-credentials` step fails with "Not authorized to perform sts:AssumeRoleWithWebIdentity". Clear and diagnosable.
- **ECR repo doesn't exist in prod account** — The `imagetools create` will fail with "repository does not exist". The ECR repo (`decisionbox-agent`) must be pre-created in the prod account (infra prerequisite).

## Risks

| Risk | Likelihood | Mitigation |
|------|-----------|------------|
| Prod secrets not configured when PR merges | Medium | First run fails loudly; no silent degradation. Document as prerequisite in PR description. |
| Prod ECR repo not created | Medium | Same — `imagetools create` fails clearly. Infra team (Onur) owns the ECR repo creation. |
| OIDC trust not configured on prod role | Low | The issue states the role ARN, implying it exists. Verify with infra. |
| Rate limiting on ECR token fetch | Very low | The two assume-role calls are seconds apart and well within AWS STS limits. |
| Workflow exceeds GitHub Actions job timeout | Very low | `imagetools create` is fast (metadata copy, not layer transfer). |

## Alternatives Considered

1. **Single role with cross-account ECR push** — Use the dev role to push to both registries via an ECR resource policy on the prod repo. Rejected: requires IAM changes on the prod account's ECR repo policy, adds cross-account coupling, and doesn't match the per-account OIDC role pattern the team has established.

2. **Separate workflow for prod push** — A second workflow triggered by the dev push completing. Rejected: adds complexity (workflow chaining, artifact passing) for no benefit. The sequential steps in one job are simpler and already proven by the dev-ECR block.

3. **Reusable workflow / composite action** — Extract the 3-step ECR push into a reusable workflow or composite action and call it twice (dev, prod). Considered but deferred: the duplication is only 3 steps with different secrets, and abstracting adds a layer without solving a real maintenance problem today. If a third account is added later, extraction would be warranted.

---

**This is a PLAN for review — implementation follows after approval.**

Closes #297
