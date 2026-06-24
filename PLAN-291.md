# Plan: Push decisionbox-agent image to ECR alongside GHCR

## Problem

The cloud platform has migrated to AWS EKS (`decisionbox-io/decisionbox-cloud-infra#47`).
Tenant agent K8s Jobs on EKS pull the `decisionbox-agent` image from ECR, but the
CI workflow currently only pushes to GHCR. The agent image must also be pushed to
`435998721348.dkr.ecr.us-east-1.amazonaws.com/decisionbox-agent:<tag>` on every
publish event.

## Proposed Approach

Add an ECR push step to the existing `push` job in `.github/workflows/docker-publish.yml`.
The step runs **only for the `decisionbox-agent` matrix entry** (a conditional on
`matrix.service`), keeps the GHCR push unchanged for all three images, and uses
GitHub Actions OIDC federation (already trusted at the AWS org level) to authenticate.

**Why modify the existing `push` job instead of adding a new job?**
- The `push` job already has the built image, buildx, and the metadata/tag outputs.
- Adding a separate job would require re-downloading artifacts or rebuilding — wasteful.
- A matrix-conditional step within the same job is the minimal change.

**Why not push all three images to ECR?**
- The issue explicitly scopes this to `decisionbox-agent` only. The API and dashboard
  are not pulled from ECR by the cloud infra today. If that changes, extending the
  conditional is trivial.

## Files to Change

| File | Change |
|------|--------|
| `.github/workflows/docker-publish.yml` | Add ECR login + tag + push steps (agent-only) in the `push` job |
| `CHANGELOG.md` | Add entry under `[Unreleased]` |
| `docs/deployment/kubernetes.md` | Note that the agent image is also available from ECR for AWS/EKS deployments |
| `docs/reference/configuration.md` | Mention ECR image URI as alternative `AGENT_IMAGE` value for EKS |

## Step-by-step Implementation

### Phase 1: Workflow changes (`.github/workflows/docker-publish.yml`)

1. **Add `id-token: write` permission to the `push` job.**
   The OIDC token exchange with AWS requires this. Current permissions are
   `contents: read` + `packages: write`; add `id-token: write`.

2. **After the existing "Build multi-arch and push" step (which pushes to GHCR),
   add three new steps — all gated with the condition:**
   ```
   if: (matrix.changed == 'true' || github.ref_type == 'tag' || github.event_name == 'workflow_dispatch') && matrix.service == 'decisionbox-agent'
   ```

   **Step A — Configure AWS credentials (OIDC):**
   ```yaml
   - name: Configure AWS credentials
     if: <condition above>
     uses: aws-actions/configure-aws-credentials@v4
     with:
       role-to-assume: ${{ secrets.AWS_DEPLOY_ROLE_ARN }}
       aws-region: us-east-1
   ```

   **Step B — Login to ECR:**
   ```yaml
   - name: Login to Amazon ECR
     if: <condition above>
     uses: aws-actions/amazon-ecr-login@v2
     with:
       registries: ${{ secrets.AWS_ACCOUNT_ID }}
   ```
   Using the official ECR login action (v2) rather than a raw `aws ecr get-login-password`
   shell command — it handles token refresh, multi-registry, and error reporting cleanly.

   **Step C — Tag and push to ECR:**
   ```yaml
   - name: Push to ECR
     if: <condition above>
     env:
       ECR_REGISTRY: ${{ secrets.AWS_ACCOUNT_ID }}.dkr.ecr.us-east-1.amazonaws.com
       ECR_REPO: decisionbox-agent
       GHCR_TAGS: ${{ steps.meta.outputs.tags }}
     run: |
       while IFS= read -r ghcr_tag; do
         tag="${ghcr_tag##*:}"
         docker buildx imagetools create \
           --tag "$ECR_REGISTRY/$ECR_REPO:$tag" \
           "$ghcr_tag"
       done <<< "$GHCR_TAGS"
   ```
   **Key design choice:** use `docker buildx imagetools create` to copy the
   multi-arch manifest directly from GHCR to ECR (no re-build, no local `docker
   tag`+`docker push` which only works for a single-platform image). This preserves
   the amd64+arm64 manifest list as-is. The step iterates over all computed tags
   (branch, semver, sha, latest) so ECR mirrors exactly what GHCR has.

### Phase 2: Documentation updates

1. **`CHANGELOG.md`** — under `[Unreleased]` → `### Added`, add:
   ```
   - **CI pushes the `decisionbox-agent` image to AWS ECR** — `.github/workflows/docker-publish.yml`. The Docker publish workflow now pushes the agent image to ECR (`<account>.dkr.ecr.us-east-1.amazonaws.com/decisionbox-agent:<tag>`) after the GHCR push, using OIDC-based AWS authentication. EKS-based deployments can pull the agent image from ECR without cross-registry authentication.
   ```

2. **`docs/deployment/kubernetes.md`** — in the agent configuration section, add a
   note that for AWS/EKS deployments the image is also available from ECR:
   ```
   AGENT_IMAGE: "<account-id>.dkr.ecr.us-east-1.amazonaws.com/decisionbox-agent:<tag>"
   ```

3. **`docs/reference/configuration.md`** — in the `AGENT_IMAGE` row, note the ECR
   alternative for EKS deployments.

## Required Secrets (pre-configured by the admin)

| Secret | Value | Purpose |
|--------|-------|---------|
| `AWS_DEPLOY_ROLE_ARN` | `arn:aws:iam::435998721348:role/dbx-cloud-dev-github-actions` | OIDC role assumption |
| `AWS_ACCOUNT_ID` | `435998721348` | ECR registry prefix |

The OIDC trust for the `decisionbox-io` GitHub org is already configured on the
AWS side. No infrastructure changes needed.

## Data / Schema / API / UI Impacts

None. This is a CI-only change — no code, no schema, no API surface, no UI.

## Test Strategy

### CI validation (automated)

- **Syntax:** The workflow YAML is validated by GitHub Actions on push to any branch.
  A push to this PR branch will confirm the workflow parses correctly and that the
  `gate`/`detect-changes`/`build`/`security` jobs still succeed (the ECR push is
  gated behind `github.event_name != 'pull_request'`, so it won't run on the PR
  itself — same as the GHCR push).

### Manual verification (post-merge)

- Push to `main` or tag a release → confirm the workflow run shows the
  "Configure AWS credentials" / "Login to Amazon ECR" / "Push to ECR" steps
  as executed (green) for the `decisionbox-agent` matrix entry, and skipped for
  the other two.
- `docker manifest inspect 435998721348.dkr.ecr.us-east-1.amazonaws.com/decisionbox-agent:<tag>`
  shows amd64+arm64 (same as GHCR).
- `decisionbox-api` and `decisionbox-dashboard` matrix entries do NOT attempt ECR
  login or push.

### Failure / edge cases

| Scenario | Expected behaviour |
|----------|--------------------|
| `AWS_DEPLOY_ROLE_ARN` secret missing | OIDC step fails → ECR push fails → job fails (desired: loud failure, not silent skip) |
| `AWS_ACCOUNT_ID` secret missing | ECR login step fails → same as above |
| OIDC trust revoked on AWS side | OIDC step returns 403 → job fails |
| ECR repo doesn't exist | Push returns "repository does not exist" error → job fails |
| GHCR push succeeds but ECR push fails | Job fails (ECR step is non-optional); GHCR image is already pushed (push happened in the prior step) — acceptable partial state, operator investigates |
| No agent changes (matrix skipped) | Entire matrix entry skipped → no ECR push (correct) |
| PR event | `push` job doesn't run at all (`github.event_name != 'pull_request'`) → no ECR interaction |

## Risks

| Risk | Mitigation |
|------|------------|
| ECR push failure blocks the whole `push` job (including GHCR) | The ECR steps are **after** the GHCR push step, so GHCR is already done. Only the job status is "failed" — GHCR images are intact. |
| Secrets not yet configured in the repo | Issue states they're ready. If not, the first run will fail loudly — easy to diagnose. |
| `imagetools create` cross-registry copy requires GHCR image to be public or authenticated | The GHCR login step runs first (existing), so Docker has credentials for both registries at the time `imagetools create` runs. |
| Future request to push additional images to ECR | Extending the matrix conditional to include them is a one-line change (e.g. `matrix.service == 'decisionbox-agent' \|\| matrix.service == 'decisionbox-api'`). |

## Alternatives Considered

1. **Separate `push-ecr` job.** Rejected: requires re-downloading the tar artifact
   or re-building from scratch. The existing `push` job already has everything needed.

2. **`docker tag` + `docker push` shell script (as in the issue's example snippet).**
   Rejected: that works for single-arch images only. The workflow pushes multi-arch
   manifests (amd64+arm64). `docker buildx imagetools create` copies the manifest
   list cross-registry without re-pulling layers locally — faster, correct, and
   doesn't require a loaded local image.

3. **Push all three images to ECR.** Rejected: out of scope per the issue. Only the
   agent is pulled from ECR today.

4. **Use a reusable workflow or composite action.** Over-engineering for a three-step
   addition gated by one matrix conditional. If a second image is added later, extract
   then.

---

*This is a PLAN for review — implementation follows after approval.*

Closes #291
