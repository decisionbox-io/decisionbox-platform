#!/usr/bin/env bash
set -euo pipefail

# ══════════════════════════════════════════════════════════════════════════════
# DecisionBox Platform — Interactive Setup Wizard
# Configures cloud infrastructure, secrets, and deploys via Terraform + Helm.
#
# Usage: ./setup.sh [--help] [--dry-run]
# ══════════════════════════════════════════════════════════════════════════════

VERSION="1.1.0"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SETUP_START=$(date +%s)
DRY_RUN=false
SPINNER_PID=""

# ─── Parse arguments ─────────────────────────────────────────────────────────

for arg in "$@"; do
  case "$arg" in
    --help|-h)
      echo "DecisionBox Platform Setup Wizard v${VERSION}"
      echo ""
      echo "Usage: ./setup.sh [OPTIONS]"
      echo ""
      echo "Options:"
      echo "  --help, -h     Show this help message"
      echo "  --dry-run      Generate config files only (no terraform apply, no helm deploy)"
      echo ""
      echo "This wizard will:"
      echo "  1. Check prerequisites (terraform, gcloud/aws, kubectl, helm)"
      echo "  2. Configure cloud provider settings"
      echo "  3. Generate Terraform variables and Helm values"
      echo "  4. Run terraform init, plan, and apply"
      echo "  5. Configure kubectl and deploy services via Helm"
      echo ""
      echo "Supported providers: GCP (available), AWS (coming soon)"
      exit 0
      ;;
    --dry-run)
      DRY_RUN=true
      ;;
    *)
      echo "Unknown argument: $arg"
      echo "Run ./setup.sh --help for usage."
      exit 1
      ;;
  esac
done

# ─── Colors (disabled if not a TTY) ──────────────────────────────────────────

if [[ -t 1 ]]; then
  RED='\033[0;31m'
  GREEN='\033[0;32m'
  YELLOW='\033[1;33m'
  CYAN='\033[0;36m'
  BLUE='\033[0;34m'
  DIM='\033[2m'
  BOLD='\033[1m'
  NC='\033[0m'
else
  RED='' GREEN='' YELLOW='' CYAN='' BLUE='' DIM='' BOLD='' NC=''
fi

# ─── Output helpers ──────────────────────────────────────────────────────────

info()    { echo -e "${CYAN}${BOLD}▸${NC} $1"; }
ok()      { echo -e "${GREEN}${BOLD}✔${NC} $1"; }
warn()    { echo -e "${YELLOW}${BOLD}⚠${NC} $1"; }
err()     { echo -e "${RED}${BOLD}✘${NC} $1"; }
dim()     { echo -e "${DIM}  $1${NC}"; }

step_header() {
  local step="$1" total="$2" title="$3"
  echo ""
  echo -e "${BOLD}━━━ Step ${step}/${total}: ${title} ━━━${NC}"
  echo ""
}

# ─── Spinner ─────────────────────────────────────────────────────────────────

spinner_start() {
  local msg="$1"
  if [[ ! -t 1 ]]; then
    echo "$msg"
    return
  fi
  local frames=("⠋" "⠙" "⠹" "⠸" "⠼" "⠴" "⠦" "⠧" "⠇" "⠏")
  local start_time=$(date +%s)
  (
    local i=0
    while true; do
      local elapsed=$(( $(date +%s) - start_time ))
      printf "\r${CYAN}%s${NC} %s ${DIM}(%ds)${NC}  " "${frames[$i]}" "$msg" "$elapsed"
      i=$(( (i + 1) % ${#frames[@]} ))
      sleep 0.1
    done
  ) &
  SPINNER_PID=$!
  disown "$SPINNER_PID" 2>/dev/null
}

spinner_stop() {
  if [[ -n "$SPINNER_PID" ]]; then
    kill "$SPINNER_PID" 2>/dev/null || true
    wait "$SPINNER_PID" 2>/dev/null || true
    SPINNER_PID=""
    printf "\r\033[2K"
  fi
}

# ─── Prompt helpers ──────────────────────────────────────────────────────────

prompt() {
  local var_name="$1" prompt_text="$2" default="${3:-}"
  if [[ -n "$default" ]]; then
    read -rp "$(echo -e "${CYAN}?${NC} ${prompt_text} ${DIM}[${default}]${NC}: ")" value
    printf -v "$var_name" '%s' "${value:-$default}"
  else
    read -rp "$(echo -e "${CYAN}?${NC} ${prompt_text}: ")" value
    while [[ -z "$value" ]]; do
      err "This field is required."
      read -rp "$(echo -e "${CYAN}?${NC} ${prompt_text}: ")" value
    done
    printf -v "$var_name" '%s' "$value"
  fi
}

prompt_choice() {
  local var_name="$1" prompt_text="$2" default="${3:-}" options="$4"
  while true; do
    prompt "$var_name" "$prompt_text" "$default"
    local val="${!var_name}"
    if echo "$options" | grep -qw "$val"; then
      return
    fi
    err "Invalid choice: ${val}. Options: ${options}"
  done
}

prompt_number() {
  local var_name="$1" prompt_text="$2" default="${3:-}"
  while true; do
    prompt "$var_name" "$prompt_text" "$default"
    local val="${!var_name}"
    if [[ "$val" =~ ^[0-9]+$ ]]; then
      return
    fi
    err "Must be a number. Got: ${val}"
  done
}

prompt_boolean() {
  local var_name="$1" prompt_text="$2" default="${3:-false}"
  while true; do
    prompt "$var_name" "$prompt_text (true/false)" "$default"
    local val="${!var_name}"
    if [[ "$val" == "true" || "$val" == "false" ]]; then
      return
    fi
    err "Must be 'true' or 'false'. Got: ${val}"
  done
}

# ─── Elapsed time ────────────────────────────────────────────────────────────

elapsed() {
  local secs=$(( $(date +%s) - SETUP_START ))
  if [[ $secs -ge 60 ]]; then
    printf "%dm%ds" $((secs / 60)) $((secs % 60))
  else
    printf "%ds" "$secs"
  fi
}

# ─── Cleanup on exit ────────────────────────────────────────────────────────

cleanup() {
  spinner_stop
  if [[ "${1:-}" == "INT" ]]; then
    echo ""
    warn "Setup cancelled by user."
    rm -f "${TF_DIR:-}/tfplan" 2>/dev/null || true
    exit 130
  fi
}

trap 'cleanup INT' INT
trap 'spinner_stop' EXIT

# ─── Prerequisites ───────────────────────────────────────────────────────────

check_tool() {
  local name="$1" install_hint="$2"
  if command -v "$name" > /dev/null 2>&1; then
    local ver
    ver=$("$name" version 2>/dev/null | head -1 || "$name" --version 2>/dev/null | head -1 || echo "installed")
    ok "${name} ${DIM}${ver}${NC}"
    return 0
  else
    err "${name} not found"
    dim "${install_hint}"
    return 1
  fi
}

# ─── Banner ──────────────────────────────────────────────────────────────────

echo ""
echo -e "${BOLD}  ╔══════════════════════════════════════════════════╗${NC}"
echo -e "${BOLD}  ║         DecisionBox Platform Setup              ║${NC}"
echo -e "${BOLD}  ║         v${VERSION}                                  ║${NC}"
echo -e "${BOLD}  ╚══════════════════════════════════════════════════╝${NC}"
echo ""

if [[ "$DRY_RUN" == "true" ]]; then
  warn "Dry-run mode: config files will be generated but nothing will be applied."
  echo ""
fi

# ══════════════════════════════════════════════════════════════════════════════
# Step 1: Prerequisites
# ══════════════════════════════════════════════════════════════════════════════

step_header 1 8 "Prerequisites"

MISSING=0
check_tool "terraform" "Install: https://developer.hashicorp.com/terraform/install" || MISSING=$((MISSING + 1))
check_tool "kubectl"   "Install: https://kubernetes.io/docs/tasks/tools/" || MISSING=$((MISSING + 1))
check_tool "helm"      "Install: https://helm.sh/docs/intro/install/" || MISSING=$((MISSING + 1))
check_tool "openssl"   "Usually pre-installed on macOS/Linux" || MISSING=$((MISSING + 1))

if [[ "$MISSING" -gt 0 ]]; then
  echo ""
  err "Missing ${MISSING} required tool(s). Install them and re-run."
  exit 1
fi

echo ""
ok "All prerequisites met"

# ══════════════════════════════════════════════════════════════════════════════
# Step 2: Cloud Provider
# ══════════════════════════════════════════════════════════════════════════════

step_header 2 8 "Cloud Provider"

echo -e "  ${BOLD}1)${NC} GCP  — Google Cloud Platform"
echo -e "  ${DIM}2)${NC} ${DIM}AWS  — Amazon Web Services (coming soon)${NC}"
echo ""
prompt_choice CLOUD_CHOICE "Select cloud provider" "1" "1 gcp GCP"

case "$CLOUD_CHOICE" in
  1|gcp|GCP) CLOUD="gcp" ;;
esac

ok "Cloud provider: ${BOLD}${CLOUD^^}${NC}"

# Check cloud CLI
echo ""
if [[ "$CLOUD" == "gcp" ]]; then
  check_tool "gcloud" "Install: https://cloud.google.com/sdk/docs/install" || {
    err "gcloud CLI is required for GCP. Install and re-run."
    exit 1
  }
fi

# ══════════════════════════════════════════════════════════════════════════════
# Step 3: Secrets Configuration
# ══════════════════════════════════════════════════════════════════════════════

step_header 3 8 "Secrets Configuration"

info "The secret namespace prefixes all secrets to avoid conflicts."
dim "Format: {namespace}-{projectID}-{key} (e.g., decisionbox-proj123-llm-api-key)"
echo ""
prompt SECRET_NS "Secret namespace" "decisionbox"
ok "Secret namespace: ${BOLD}${SECRET_NS}${NC}"

echo ""
CLOUD_UPPER="${CLOUD^^}"
echo -e "  ${BOLD}1)${NC} Enable  — Use ${CLOUD_UPPER} Secret Manager ${DIM}(recommended for production)${NC}"
echo -e "  ${BOLD}2)${NC} Disable — Use MongoDB encrypted secrets or K8s native secrets"
echo ""
prompt_choice SECRETS_CHOICE "Enable cloud secret manager?" "1" "1 2 yes y no n"

case "$SECRETS_CHOICE" in
  1|yes|y) ENABLE_SECRETS="true" ;;
  2|no|n)  ENABLE_SECRETS="false" ;;
esac

ok "Cloud secret manager: ${BOLD}${ENABLE_SECRETS}${NC}"

# ══════════════════════════════════════════════════════════════════════════════
# Step 4: Provider-Specific Configuration
# ══════════════════════════════════════════════════════════════════════════════

if [[ "$CLOUD" == "gcp" ]]; then
  step_header 4 8 "GCP Configuration"

  TF_DIR="${SCRIPT_DIR}/gcp/prod"

  prompt PROJECT_ID "GCP project ID"

  # Validate project ID format
  if [[ ! "$PROJECT_ID" =~ ^[a-z][a-z0-9-]{4,28}[a-z0-9]$ ]]; then
    warn "Project ID '${PROJECT_ID}' may not match GCP naming rules (lowercase letters, digits, hyphens, 6-30 chars)."
    dim "Continuing anyway — Terraform will validate against the API."
  fi

  prompt REGION "GCP region" "us-central1"
  prompt CLUSTER_NAME "GKE cluster name" "decisionbox-prod"
  prompt K8S_NS "Kubernetes namespace" "decisionbox"

  echo ""
  info "Node pool configuration:"
  prompt MACHINE_TYPE "Machine type" "e2-standard-2"
  prompt_number MIN_NODES "Min nodes per zone" "1"
  prompt_number MAX_NODES "Max nodes per zone" "2"

  # Validate min <= max
  if [[ "$MIN_NODES" -gt "$MAX_NODES" ]]; then
    err "Min nodes (${MIN_NODES}) cannot be greater than max nodes (${MAX_NODES})."
    exit 1
  fi

  echo ""
  prompt_boolean BQ_IAM "Enable BigQuery IAM for data warehouse access?" "false"

  # ─── Terraform State ─────────────────────────────────────────────────

  step_header 5 8 "Terraform State"

  info "Terraform state must be stored in a GCS bucket for persistence and team collaboration."
  echo ""
  prompt TF_STATE_BUCKET "GCS bucket name" "${PROJECT_ID}-terraform-state"
  prompt TF_STATE_PREFIX "State prefix (environment)" "prod"

  if [[ "$DRY_RUN" == "false" ]]; then
    if gcloud storage buckets describe "gs://${TF_STATE_BUCKET}" --project="$PROJECT_ID" > /dev/null 2>&1; then
      ok "Bucket gs://${TF_STATE_BUCKET} already exists"
    else
      spinner_start "Creating bucket gs://${TF_STATE_BUCKET}..."
      gcloud storage buckets create "gs://${TF_STATE_BUCKET}" \
        --project="$PROJECT_ID" \
        --location="$REGION" \
        --uniform-bucket-level-access \
        --public-access-prevention > /dev/null 2>&1
      gcloud storage buckets update "gs://${TF_STATE_BUCKET}" --versioning > /dev/null 2>&1
      spinner_stop
      ok "Created bucket gs://${TF_STATE_BUCKET} with versioning"
    fi
  else
    dim "Dry-run: skipping bucket creation"
  fi

elif [[ "$CLOUD" == "aws" ]]; then
  step_header 4 8 "AWS Configuration"

  TF_DIR="${SCRIPT_DIR}/aws/prod"

  if [[ ! -d "$TF_DIR" ]]; then
    echo ""
    warn "AWS Terraform module is not yet available."
    echo ""
    info "The AWS secrets provider is implemented and ready:"
    dim "providers/secrets/aws/"
    echo ""
    info "To use AWS Secrets Manager, set these environment variables in your deployment:"
    echo ""
    echo -e "  ${CYAN}SECRET_PROVIDER${NC}=aws"
    echo -e "  ${CYAN}SECRET_NAMESPACE${NC}=${SECRET_NS}"
    echo -e "  ${CYAN}SECRET_AWS_REGION${NC}=us-east-1"
    echo ""
    info "Ensure the pod's IAM role has SecretsManager permissions scoped to:"
    echo -e "  ${DIM}arn:aws:secretsmanager:<region>:<account>:secret:${SECRET_NS}-*${NC}"
    echo ""
    info "Track progress: https://github.com/decisionbox-io/decisionbox-platform/issues/39"
    echo ""
    exit 0
  fi
fi

# ══════════════════════════════════════════════════════════════════════════════
# Step 6: Review Configuration
# ══════════════════════════════════════════════════════════════════════════════

step_header 6 8 "Review Configuration"

echo -e "  ${BOLD}Cloud:${NC}              ${CLOUD^^}"
echo -e "  ${BOLD}Secret namespace:${NC}   ${SECRET_NS}"
echo -e "  ${BOLD}Cloud secrets:${NC}      ${ENABLE_SECRETS}"
echo ""

if [[ "$CLOUD" == "gcp" ]]; then
  echo -e "  ${BOLD}GCP project:${NC}        ${PROJECT_ID}"
  echo -e "  ${BOLD}Region:${NC}             ${REGION}"
  echo -e "  ${BOLD}Cluster:${NC}            ${CLUSTER_NAME}"
  echo -e "  ${BOLD}K8s namespace:${NC}      ${K8S_NS}"
  echo -e "  ${BOLD}Machine type:${NC}       ${MACHINE_TYPE}"
  echo -e "  ${BOLD}Nodes:${NC}              ${MIN_NODES}-${MAX_NODES} per zone"
  echo -e "  ${BOLD}BigQuery IAM:${NC}       ${BQ_IAM}"
  echo -e "  ${BOLD}State bucket:${NC}       gs://${TF_STATE_BUCKET}/${TF_STATE_PREFIX}/"
fi

echo ""
prompt CONFIRM "Proceed with this configuration? (yes/no)" "yes"

if [[ "$CONFIRM" != "yes" ]]; then
  warn "Setup cancelled. Re-run to start over."
  exit 0
fi

# ══════════════════════════════════════════════════════════════════════════════
# Step 7: Generate Config Files
# ══════════════════════════════════════════════════════════════════════════════

step_header 7 8 "Generate Config Files"

if [[ "$CLOUD" == "gcp" ]]; then
  # ─── terraform.tfvars ──────────────────────────────────────────────
  TFVARS_FILE="${TF_DIR}/terraform.tfvars"

  cat > "$TFVARS_FILE" <<EOF
# Generated by setup.sh v${VERSION} on $(date -u +"%Y-%m-%dT%H:%M:%SZ")

project_id   = "${PROJECT_ID}"
region       = "${REGION}"
cluster_name = "${CLUSTER_NAME}"

# GKE node pool
machine_type   = "${MACHINE_TYPE}"
min_node_count = ${MIN_NODES}
max_node_count = ${MAX_NODES}

# Workload Identity
k8s_namespace = "${K8S_NS}"

# Optional features
enable_gcp_secrets  = ${ENABLE_SECRETS}
secret_namespace    = "${SECRET_NS}"
enable_bigquery_iam = ${BQ_IAM}
EOF

  ok "Generated ${TFVARS_FILE}"

  # ─── Helm values override ──────────────────────────────────────────
  HELM_DIR="${SCRIPT_DIR}/../helm-charts/decisionbox-api"
  HELM_VALUES="${HELM_DIR}/values-secrets.yaml"

  K8S_SA="decisionbox-api"
  GCP_SA="${CLUSTER_NAME}-api@${PROJECT_ID}.iam.gserviceaccount.com"

  cat > "$HELM_VALUES" <<EOF
# Generated by setup.sh v${VERSION} on $(date -u +"%Y-%m-%dT%H:%M:%SZ")
# Usage: helm upgrade --install decisionbox-api ./helm-charts/decisionbox-api -f values.yaml -f values-secrets.yaml

namespace: ${K8S_NS}

serviceAccountName: ${K8S_SA}
serviceAccountAnnotations:
  iam.gke.io/gcp-service-account: "${GCP_SA}"

automountServiceAccountToken: true

extraEnvFrom:
  - secretRef:
      name: decisionbox-api-secrets

env:
  SECRET_PROVIDER: "gcp"
  SECRET_NAMESPACE: "${SECRET_NS}"
  SECRET_GCP_PROJECT_ID: "${PROJECT_ID}"
EOF

  ok "Generated ${HELM_VALUES}"
fi

if [[ "$DRY_RUN" == "true" ]]; then
  echo ""
  ok "Dry-run complete. Config files generated. No infrastructure changes made."
  echo ""
  dim "To apply manually:"
  dim "  cd ${TF_DIR}"
  dim "  terraform init -backend-config=\"bucket=${TF_STATE_BUCKET}\" -backend-config=\"prefix=${TF_STATE_PREFIX}\""
  dim "  terraform plan -out=tfplan"
  dim "  terraform apply tfplan"
  echo ""
  echo -e "  ${DIM}Total time: $(elapsed)${NC}"
  exit 0
fi

# ══════════════════════════════════════════════════════════════════════════════
# Step 8: Terraform & Deploy
# ══════════════════════════════════════════════════════════════════════════════

step_header 8 8 "Terraform & Deploy"

cd "$TF_DIR"
dim "Working directory: ${TF_DIR}"
echo ""

# ─── Terraform Init ──────────────────────────────────────────────────────

spinner_start "Running terraform init..."
TF_INIT_ARGS=(-input=false -backend-config="bucket=${TF_STATE_BUCKET}" -backend-config="prefix=${TF_STATE_PREFIX}")
TF_INIT_OUTPUT=$(terraform init "${TF_INIT_ARGS[@]}" 2>&1) && TF_INIT_RC=0 || TF_INIT_RC=$?
spinner_stop

if [[ "$TF_INIT_RC" -ne 0 ]]; then
  err "Terraform init failed:"
  echo "$TF_INIT_OUTPUT"
  exit 1
fi
ok "Terraform initialized ${DIM}(state: gs://${TF_STATE_BUCKET}/${TF_STATE_PREFIX}/)${NC}"

# ─── Terraform Plan ──────────────────────────────────────────────────────

echo ""
info "Running terraform plan..."
echo ""
terraform plan -out=tfplan -detailed-exitcode 2>&1 && TF_EXIT=0 || TF_EXIT=$?
echo ""

if [[ "$TF_EXIT" -eq 1 ]]; then
  err "Terraform plan failed. Review the errors above."
  rm -f tfplan
  exit 1
elif [[ "$TF_EXIT" -eq 0 ]]; then
  ok "No infrastructure changes needed."
  rm -f tfplan
  TF_APPLIED="skip"
else
  ok "Plan saved to tfplan"
  echo ""
  prompt APPLY "Apply these changes? (yes/no)" "no"

  if [[ "$APPLY" == "yes" ]]; then
    echo ""
    TF_APPLY_START=$(date +%s)
    info "Applying (this may take 5-10 minutes for new clusters)..."
    echo ""
    terraform apply tfplan
    TF_APPLY_SECS=$(( $(date +%s) - TF_APPLY_START ))
    echo ""
    ok "Applied successfully! ${DIM}(${TF_APPLY_SECS}s)${NC}"
    TF_APPLIED="yes"
  else
    TF_APPLIED="no"
    info "Skipped apply. Run manually: cd ${TF_DIR} && terraform apply tfplan"
  fi
  rm -f tfplan
fi

# ─── Configure kubectl ───────────────────────────────────────────────────

if [[ "$CLOUD" == "gcp" ]]; then
  echo ""
  spinner_start "Fetching cluster credentials..."
  gcloud container clusters get-credentials "$CLUSTER_NAME" \
    --region "$REGION" \
    --project "$PROJECT_ID" 2>/dev/null
  spinner_stop
  ok "kubectl configured for ${CLUSTER_NAME}"

  spinner_start "Waiting for Kubernetes API..."
  RETRIES=0
  MAX_RETRIES=30
  until kubectl get nodes > /dev/null 2>&1; do
    RETRIES=$((RETRIES + 1))
    if [[ "$RETRIES" -ge "$MAX_RETRIES" ]]; then
      spinner_stop
      err "Kubernetes API not reachable after ${MAX_RETRIES} attempts (${MAX_RETRIES}0s)."
      err "Check: gcloud container clusters list --project=${PROJECT_ID}"
      exit 1
    fi
    sleep 10
  done
  spinner_stop
  ok "Kubernetes API is ready"
fi

# ─── Helm Deploy ─────────────────────────────────────────────────────────

HELM_CHARTS_DIR="${SCRIPT_DIR}/../helm-charts"

echo ""
prompt HELM_DEPLOY "Deploy services via Helm? (yes/no)" "no"

if [[ "$HELM_DEPLOY" == "yes" ]]; then

  # ─── Create API Secrets ────────────────────────────────────────────
  API_SECRET_NAME="decisionbox-api-secrets"
  if kubectl get secret "$API_SECRET_NAME" -n "$K8S_NS" > /dev/null 2>&1; then
    ok "Secret ${API_SECRET_NAME} already exists"
  else
    AUTO_KEY=$(openssl rand -base64 32)
    echo ""
    info "SECRET_ENCRYPTION_KEY is used for AES-256 encryption of project secrets."
    dim "Press Enter to use the auto-generated key, or paste your own."
    prompt ENCRYPTION_KEY "SECRET_ENCRYPTION_KEY" "$AUTO_KEY"
    kubectl create namespace "$K8S_NS" --dry-run=client -o yaml | kubectl apply -f - > /dev/null 2>&1
    kubectl create secret generic "$API_SECRET_NAME" \
      --from-literal=SECRET_ENCRYPTION_KEY="$ENCRYPTION_KEY" \
      -n "$K8S_NS" > /dev/null 2>&1
    ok "Created secret ${API_SECRET_NAME}"
  fi

  echo ""
  prompt HELM_VALUES_ENV "Additional API values file (leave empty to skip)" "none"

  # ─── Deploy API ────────────────────────────────────────────────────
  echo ""
  spinner_start "Deploying API..."
  HELM_ARGS=(helm upgrade --install decisionbox-api "$HELM_DIR" -n "$K8S_NS" --create-namespace -f "${HELM_DIR}/values.yaml")
  if [[ "$CLOUD" == "gcp" ]]; then
    HELM_ARGS+=(-f "$HELM_VALUES")
  fi
  if [[ "$HELM_VALUES_ENV" != "none" && -n "$HELM_VALUES_ENV" ]]; then
    if [[ ! "$HELM_VALUES_ENV" = /* ]]; then
      HELM_VALUES_ENV="${SCRIPT_DIR}/../${HELM_VALUES_ENV}"
    fi
    if [[ ! -f "$HELM_VALUES_ENV" ]]; then
      spinner_stop
      err "Values file not found: ${HELM_VALUES_ENV}"
      exit 1
    fi
    HELM_ARGS+=(-f "$HELM_VALUES_ENV")
  fi
  HELM_OUTPUT=$("${HELM_ARGS[@]}" 2>&1) && HELM_RC=0 || HELM_RC=$?
  spinner_stop

  if [[ "$HELM_RC" -ne 0 ]]; then
    err "API deployment failed:"
    echo "$HELM_OUTPUT"
    exit 1
  fi
  ok "API deployed"

  # ─── Deploy Dashboard ──────────────────────────────────────────────
  DASH_DIR="${HELM_CHARTS_DIR}/decisionbox-dashboard"
  spinner_start "Deploying Dashboard..."
  DASH_ARGS=(helm upgrade --install decisionbox-dashboard "$DASH_DIR" -n "$K8S_NS" --create-namespace -f "${DASH_DIR}/values.yaml" --set "namespace=${K8S_NS}")
  DASH_OUTPUT=$("${DASH_ARGS[@]}" 2>&1) && DASH_RC=0 || DASH_RC=$?
  spinner_stop

  if [[ "$DASH_RC" -ne 0 ]]; then
    err "Dashboard deployment failed:"
    echo "$DASH_OUTPUT"
    exit 1
  fi
  ok "Dashboard deployed"

  # ─── Wait for Ingress ──────────────────────────────────────────────

  echo ""
  info "Waiting for dashboard to become available..."
  echo ""

  # Wait for ingress resource (handles GCE cleanup race)
  spinner_start "Waiting for ingress resource..."
  INGRESS_ATTEMPTS=0
  MAX_INGRESS_ATTEMPTS=3
  while true; do
    RETRIES=0
    INGRESS_FOUND=false
    while [[ "$RETRIES" -lt 12 ]]; do
      if kubectl get ingress -n "$K8S_NS" -o name 2>/dev/null | grep -q .; then
        INGRESS_FOUND=true
        break
      fi
      RETRIES=$((RETRIES + 1))
      sleep 5
    done

    if [[ "$INGRESS_FOUND" == "true" ]]; then
      sleep 10
      if kubectl get ingress -n "$K8S_NS" -o name 2>/dev/null | grep -q .; then
        break
      fi
    fi

    INGRESS_ATTEMPTS=$((INGRESS_ATTEMPTS + 1))
    if [[ "$INGRESS_ATTEMPTS" -ge "$MAX_INGRESS_ATTEMPTS" ]]; then
      spinner_stop
      warn "Ingress not created after ${MAX_INGRESS_ATTEMPTS} attempts."
      dim "Check: kubectl get ingress -n ${K8S_NS}"
      break
    fi
    "${DASH_ARGS[@]}" > /dev/null 2>&1 || true
  done
  spinner_stop
  ok "Ingress resource exists"

  # Wait for external IP
  spinner_start "Waiting for external IP (1-2 min)..."
  RETRIES=0
  INGRESS_IP=""
  while [[ -z "$INGRESS_IP" || "$INGRESS_IP" == "null" ]]; do
    RETRIES=$((RETRIES + 1))
    if [[ "$RETRIES" -ge 30 ]]; then
      spinner_stop
      warn "IP not assigned after 5 minutes."
      dim "Check: kubectl get ingress -n ${K8S_NS}"
      break
    fi
    if ! kubectl get ingress -n "$K8S_NS" -o name 2>/dev/null | grep -q .; then
      "${DASH_ARGS[@]}" > /dev/null 2>&1 || true
      sleep 15
      continue
    fi
    INGRESS_IP=$(kubectl get ingress -n "$K8S_NS" -o jsonpath='{.items[0].status.loadBalancer.ingress[0].ip}' 2>/dev/null || echo "")
    if [[ -z "$INGRESS_IP" || "$INGRESS_IP" == "null" ]]; then
      sleep 10
    fi
  done
  spinner_stop

  if [[ -n "$INGRESS_IP" && "$INGRESS_IP" != "null" ]]; then
    ok "Ingress IP: ${BOLD}${INGRESS_IP}${NC}"

    # Wait for healthy backends
    spinner_start "Waiting for health checks (3-5 min)..."
    RETRIES=0
    while true; do
      RETRIES=$((RETRIES + 1))
      if [[ "$RETRIES" -ge 40 ]]; then
        spinner_stop
        warn "Health checks not passing after 7 minutes."
        dim "Check: kubectl describe ingress -n ${K8S_NS}"
        break
      fi
      BACKENDS=$(kubectl get ingress -n "$K8S_NS" -o jsonpath='{.items[0].metadata.annotations.ingress\.kubernetes\.io/backends}' 2>/dev/null || echo "")
      if [[ -n "$BACKENDS" ]] && ! echo "$BACKENDS" | grep -q "Unknown\|UNHEALTHY"; then
        spinner_stop
        ok "All backends healthy"
        break
      fi
      sleep 10
    done

    # Verify HTTP 200
    spinner_start "Verifying dashboard is reachable..."
    RETRIES=0
    DASHBOARD_LIVE=false
    while [[ "$RETRIES" -lt 18 ]]; do
      RETRIES=$((RETRIES + 1))
      HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" --connect-timeout 5 "http://${INGRESS_IP}/" 2>/dev/null || echo "000")
      if [[ "$HTTP_CODE" == "200" ]]; then
        DASHBOARD_LIVE=true
        break
      fi
      sleep 10
    done
    spinner_stop

    if [[ "$DASHBOARD_LIVE" == "true" ]]; then
      ok "Dashboard is live!"
    else
      warn "Dashboard not responding yet. The load balancer may still be propagating."
      dim "Try: curl http://${INGRESS_IP}"
    fi

    # ─── Final Summary ──────────────────────────────────────────────
    echo ""
    echo -e "  ${GREEN}${BOLD}╔══════════════════════════════════════════════════╗${NC}"
    echo -e "  ${GREEN}${BOLD}║              Setup Complete!                     ║${NC}"
    echo -e "  ${GREEN}${BOLD}╚══════════════════════════════════════════════════╝${NC}"
    echo ""
    echo -e "  ${BOLD}Dashboard:${NC}  http://${INGRESS_IP}"
    echo -e "  ${BOLD}API:${NC}        http://decisionbox-api-service.${K8S_NS}:8080 ${DIM}(cluster-internal)${NC}"
    echo -e "  ${BOLD}Namespace:${NC}  ${K8S_NS}"
    echo ""
    echo -e "  ${DIM}Total time: $(elapsed)${NC}"
    echo ""
  fi
else
  echo ""
  info "Skipped Helm deploy. To deploy manually:"
  echo ""
  if [[ "$CLOUD" == "gcp" ]]; then
    echo -e "  ${BOLD}# API${NC}"
    echo -e "  ${DIM}helm upgrade --install decisionbox-api ${HELM_DIR} \\${NC}"
    echo -e "  ${DIM}  -f ${HELM_DIR}/values.yaml \\${NC}"
    echo -e "  ${DIM}  -f ${HELM_VALUES} -n ${K8S_NS}${NC}"
  fi
  echo ""
  echo -e "  ${BOLD}# Dashboard${NC}"
  echo -e "  ${DIM}helm upgrade --install decisionbox-dashboard ${HELM_CHARTS_DIR}/decisionbox-dashboard \\${NC}"
  echo -e "  ${DIM}  -f ${HELM_CHARTS_DIR}/decisionbox-dashboard/values.yaml -n ${K8S_NS}${NC}"
  echo ""
  echo -e "  ${DIM}Total time: $(elapsed)${NC}"
fi
