#!/usr/bin/env bash
set -euo pipefail

# DecisionBox Platform — Interactive Terraform Setup
# Configures cloud provider, secrets, and runs terraform plan/apply.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
BOLD='\033[1m'
NC='\033[0m'

info()  { echo -e "${CYAN}${BOLD}▸${NC} $1"; }
ok()    { echo -e "${GREEN}${BOLD}✔${NC} $1"; }
warn()  { echo -e "${YELLOW}${BOLD}⚠${NC} $1"; }
err()   { echo -e "${RED}${BOLD}✘${NC} $1"; }
header(){ echo -e "\n${BOLD}━━━ $1 ━━━${NC}\n"; }

prompt() {
  local var_name="$1" prompt_text="$2" default="${3:-}"
  if [[ -n "$default" ]]; then
    read -rp "$(echo -e "${CYAN}?${NC} ${prompt_text} [${default}]: ")" value
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

# ─── Cloud Provider ──────────────────────────────────────────────────────────

header "DecisionBox Platform Setup"

echo -e "  ${BOLD}1)${NC} GCP  — Google Cloud Platform"
echo -e "  ${BOLD}2)${NC} AWS  — Amazon Web Services"
echo ""
prompt CLOUD_CHOICE "Select cloud provider (1 or 2)" "1"

case "$CLOUD_CHOICE" in
  1|gcp|GCP) CLOUD="gcp" ;;
  2|aws|AWS) CLOUD="aws" ;;
  *) err "Invalid choice. Exiting."; exit 1 ;;
esac

ok "Cloud provider: ${BOLD}${CLOUD}${NC}"

# ─── Secrets Configuration ───────────────────────────────────────────────────

header "Secrets Configuration"

info "The secret namespace prefixes all secrets to avoid conflicts."
info "Format: {namespace}-{projectID}-{key} (e.g., decisionbox-proj123-llm-api-key)"
echo ""
prompt SECRET_NS "Secret namespace" "decisionbox"
ok "Secret namespace: ${BOLD}${SECRET_NS}${NC}"

echo ""
CLOUD_UPPER="$(echo "$CLOUD" | tr '[:lower:]' '[:upper:]')"
echo -e "  ${BOLD}1)${NC} Enable  — Use ${CLOUD_UPPER} Secret Manager (recommended for production)"
echo -e "  ${BOLD}2)${NC} Disable — Use MongoDB encrypted secrets or K8s native secrets"
echo ""
prompt SECRETS_CHOICE "Enable cloud secret manager? (1 or 2)" "1"

case "$SECRETS_CHOICE" in
  1|yes|y) ENABLE_SECRETS="true" ;;
  2|no|n)  ENABLE_SECRETS="false" ;;
  *) err "Invalid choice. Exiting."; exit 1 ;;
esac

ok "Cloud secret manager: ${BOLD}${ENABLE_SECRETS}${NC}"

# ─── Provider-Specific Configuration ─────────────────────────────────────────

if [[ "$CLOUD" == "gcp" ]]; then
  header "GCP Configuration"

  TF_DIR="${SCRIPT_DIR}/gcp/prod"

  prompt PROJECT_ID "GCP project ID"
  prompt REGION "GCP region" "us-central1"
  prompt CLUSTER_NAME "GKE cluster name" "decisionbox-prod"
  prompt K8S_NS "Kubernetes namespace (used for both Terraform WI binding and Helm deploy)" "decisionbox"

  # ─── Terraform State Bucket ─────────────────────────────────────────
  header "Terraform State"

  info "Terraform state must be stored in a GCS bucket for persistence and team collaboration."
  echo ""
  prompt TF_STATE_BUCKET "GCS bucket name for Terraform state" "${PROJECT_ID}-terraform-state"
  prompt TF_STATE_PREFIX "State prefix (environment name)" "prod"

  # Check if bucket exists, create if not
  if gcloud storage buckets describe "gs://${TF_STATE_BUCKET}" --project="$PROJECT_ID" > /dev/null 2>&1; then
    ok "Bucket gs://${TF_STATE_BUCKET} already exists"
  else
    info "Creating bucket gs://${TF_STATE_BUCKET}..."
    gcloud storage buckets create "gs://${TF_STATE_BUCKET}" \
      --project="$PROJECT_ID" \
      --location="$REGION" \
      --uniform-bucket-level-access \
      --public-access-prevention
    # Enable versioning for state recovery
    gcloud storage buckets update "gs://${TF_STATE_BUCKET}" --versioning
    ok "Created bucket gs://${TF_STATE_BUCKET} with versioning enabled"
  fi

  header "GKE Node Pool"
  prompt MACHINE_TYPE "Machine type" "e2-standard-2"
  prompt MIN_NODES "Min nodes per zone" "1"
  prompt MAX_NODES "Max nodes per zone" "2"

  header "Optional Features"
  prompt BQ_IAM "Enable BigQuery IAM? (true/false)" "false"

  # ─── Generate terraform.tfvars ───────────────────────────────────────────
  TFVARS_FILE="${TF_DIR}/terraform.tfvars"

  cat > "$TFVARS_FILE" <<EOF
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

  # ─── Generate Helm values override ───────────────────────────────────────
  HELM_DIR="${SCRIPT_DIR}/../helm-charts/decisionbox-api"
  HELM_VALUES="${HELM_DIR}/values-secrets.yaml"

  K8S_SA="decisionbox-api"
  GCP_SA="${CLUSTER_NAME}-api@${PROJECT_ID}.iam.gserviceaccount.com"

  cat > "$HELM_VALUES" <<EOF
# Generated by setup.sh — secret provider configuration
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

elif [[ "$CLOUD" == "aws" ]]; then
  header "AWS Configuration"

  TF_DIR="${SCRIPT_DIR}/aws/prod"

  if [[ ! -d "$TF_DIR" ]]; then
    warn "AWS Terraform module not yet available at ${TF_DIR}"
    info "AWS secrets provider is implemented in the API (providers/secrets/aws/)"
    info "Terraform module for AWS is coming soon."
    echo ""
    info "For now, set these environment variables in your K8s deployment:"
    echo ""
    echo -e "  ${CYAN}SECRET_PROVIDER${NC}=aws"
    echo -e "  ${CYAN}SECRET_NAMESPACE${NC}=${SECRET_NS}"
    echo -e "  ${CYAN}SECRET_AWS_REGION${NC}=us-east-1"
    echo ""
    info "And ensure the pod's IAM role has SecretsManager permissions scoped to:"
    echo -e "  arn:aws:secretsmanager:<region>:<account>:secret:${SECRET_NS}-*"
    echo ""
    exit 0
  fi
fi

# ─── Terraform Init & Plan ───────────────────────────────────────────────────

header "Terraform"

cd "$TF_DIR"
info "Working directory: ${TF_DIR}"
echo ""

# Init with remote backend
info "Running terraform init..."
TF_INIT_ARGS=(-input=false -backend-config="bucket=${TF_STATE_BUCKET}" -backend-config="prefix=${TF_STATE_PREFIX}")
if ! terraform init "${TF_INIT_ARGS[@]}" > /dev/null 2>&1; then
  warn "terraform init failed, running with output..."
  terraform init "${TF_INIT_ARGS[@]}"
fi
ok "Terraform initialized (state: gs://${TF_STATE_BUCKET}/${TF_STATE_PREFIX}/)"
echo ""

# Plan — capture to detect no-op
info "Running terraform plan..."
echo ""
terraform plan -out=tfplan -detailed-exitcode 2>&1 && TF_EXIT=0 || TF_EXIT=$?
echo ""

# Exit codes: 0 = no changes, 1 = error, 2 = changes present
if [[ "$TF_EXIT" -eq 1 ]]; then
  err "Terraform plan failed."
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
    info "Applying..."
    terraform apply tfplan
    echo ""
    ok "Applied successfully!"
    TF_APPLIED="yes"
  else
    TF_APPLIED="no"
  fi
  rm -f tfplan
fi

# ─── Configure kubectl ────────────────────────────────────────────────────────

if [[ "$CLOUD" == "gcp" ]]; then
  header "Kubernetes Credentials"
  info "Fetching cluster credentials..."
  gcloud container clusters get-credentials "$CLUSTER_NAME" \
    --region "$REGION" \
    --project "$PROJECT_ID"
  ok "kubectl configured for ${CLUSTER_NAME}"

  info "Waiting for Kubernetes API to be ready..."
  RETRIES=0
  MAX_RETRIES=30
  until kubectl get nodes > /dev/null 2>&1; do
    RETRIES=$((RETRIES + 1))
    if [[ "$RETRIES" -ge "$MAX_RETRIES" ]]; then
      err "Kubernetes API not reachable after ${MAX_RETRIES} attempts."
      exit 1
    fi
    echo -n "."
    sleep 10
  done
  echo ""
  ok "Kubernetes API is ready"
fi

# ─── Helm Deploy ──────────────────────────────────────────────────────────────

HELM_CHARTS_DIR="${SCRIPT_DIR}/../helm-charts"

header "Helm Deploy"

if [[ "$CLOUD" == "gcp" ]]; then
  info "API values file generated at: ${HELM_VALUES}"
fi
echo ""
prompt HELM_DEPLOY "Deploy services via Helm? (yes/no)" "no"

if [[ "$HELM_DEPLOY" == "yes" ]]; then
  # ─── Create API Secrets ───────────────────────────────────────────────
  API_SECRET_NAME="decisionbox-api-secrets"
  if kubectl get secret "$API_SECRET_NAME" -n "$K8S_NS" > /dev/null 2>&1; then
    ok "Secret ${API_SECRET_NAME} already exists"
  else
    info "Generating SECRET_ENCRYPTION_KEY (AES-256)..."
    ENCRYPTION_KEY=$(openssl rand -base64 32)
    kubectl create namespace "$K8S_NS" --dry-run=client -o yaml | kubectl apply -f -
    kubectl create secret generic "$API_SECRET_NAME" \
      --from-literal=SECRET_ENCRYPTION_KEY="$ENCRYPTION_KEY" \
      -n "$K8S_NS"
    ok "Created secret ${API_SECRET_NAME} with SECRET_ENCRYPTION_KEY"
  fi
  echo ""
  prompt HELM_VALUES_ENV "Additional API values file, e.g. ${HELM_DIR}/values-prod.yaml (leave empty to skip)" "none"

  # ─── Deploy API ──────────────────────────────────────────────────────
  info "Deploying API..."
  HELM_ARGS=(helm upgrade --install decisionbox-api "$HELM_DIR" -n "$K8S_NS" --create-namespace -f "${HELM_DIR}/values.yaml")
  if [[ "$CLOUD" == "gcp" ]]; then
    HELM_ARGS+=(-f "$HELM_VALUES")
  fi
  if [[ "$HELM_VALUES_ENV" != "none" && -n "$HELM_VALUES_ENV" ]]; then
    if [[ ! "$HELM_VALUES_ENV" = /* ]]; then
      HELM_VALUES_ENV="${SCRIPT_DIR}/../${HELM_VALUES_ENV}"
    fi
    HELM_ARGS+=(-f "$HELM_VALUES_ENV")
  fi
  info "Running: ${HELM_ARGS[*]}"
  "${HELM_ARGS[@]}"
  echo ""
  ok "API deployed!"

  # ─── Deploy Dashboard ────────────────────────────────────────────────
  DASH_DIR="${HELM_CHARTS_DIR}/decisionbox-dashboard"
  info "Deploying Dashboard..."
  DASH_ARGS=(helm upgrade --install decisionbox-dashboard "$DASH_DIR" -n "$K8S_NS" --create-namespace -f "${DASH_DIR}/values.yaml" --set "namespace=${K8S_NS}")
  info "Running: ${DASH_ARGS[*]}"
  "${DASH_ARGS[@]}"
  echo ""
  ok "Dashboard deployed!"

  # ─── Wait for Ingress ─────────────────────────────────────────────────
  header "Waiting for Dashboard"

  # Step 1: Wait for the ingress resource to exist.
  # After helm uninstall + install, the GCE ingress controller may still be
  # cleaning up old LB resources and can delete the newly created ingress.
  # If that happens, re-run helm upgrade to recreate it.
  info "Waiting for ingress resource..."
  INGRESS_ATTEMPTS=0
  MAX_INGRESS_ATTEMPTS=3
  while true; do
    RETRIES=0
    MAX_RETRIES=12
    INGRESS_FOUND=false
    while [[ "$RETRIES" -lt "$MAX_RETRIES" ]]; do
      if kubectl get ingress -n "$K8S_NS" -o name 2>/dev/null | grep -q .; then
        INGRESS_FOUND=true
        break
      fi
      RETRIES=$((RETRIES + 1))
      echo -n "."
      sleep 5
    done
    echo ""

    if [[ "$INGRESS_FOUND" == "true" ]]; then
      ok "Ingress resource exists"
      # Verify it persists (GCE controller may delete it during cleanup)
      sleep 10
      if kubectl get ingress -n "$K8S_NS" -o name 2>/dev/null | grep -q .; then
        break
      fi
      warn "Ingress was deleted by GCE controller (old LB cleanup in progress)"
    fi

    INGRESS_ATTEMPTS=$((INGRESS_ATTEMPTS + 1))
    if [[ "$INGRESS_ATTEMPTS" -ge "$MAX_INGRESS_ATTEMPTS" ]]; then
      warn "Ingress could not be created after ${MAX_INGRESS_ATTEMPTS} attempts."
      info "Check manually: kubectl get ingress -n ${K8S_NS}"
      break
    fi
    info "Re-deploying dashboard (attempt $((INGRESS_ATTEMPTS + 1))/${MAX_INGRESS_ATTEMPTS})..."
    "${DASH_ARGS[@]}" > /dev/null 2>&1
  done

  # Step 2: Wait for external IP
  info "Waiting for ingress IP (this can take 1-2 minutes)..."
  RETRIES=0
  MAX_RETRIES=30
  INGRESS_IP=""
  while [[ -z "$INGRESS_IP" || "$INGRESS_IP" == "null" ]]; do
    RETRIES=$((RETRIES + 1))
    if [[ "$RETRIES" -ge "$MAX_RETRIES" ]]; then
      warn "Ingress IP not assigned after ${MAX_RETRIES} attempts."
      info "Check manually: kubectl get ingress -n ${K8S_NS}"
      break
    fi
    # Verify the ingress still exists (GCE cleanup race)
    if ! kubectl get ingress -n "$K8S_NS" -o name 2>/dev/null | grep -q .; then
      warn "Ingress disappeared — re-deploying dashboard..."
      "${DASH_ARGS[@]}" > /dev/null 2>&1
      sleep 15
      continue
    fi
    INGRESS_IP=$(kubectl get ingress -n "$K8S_NS" -o jsonpath='{.items[0].status.loadBalancer.ingress[0].ip}' 2>/dev/null || echo "")
    if [[ -z "$INGRESS_IP" || "$INGRESS_IP" == "null" ]]; then
      echo -n "."
      sleep 10
    fi
  done
  echo ""

  if [[ -n "$INGRESS_IP" && "$INGRESS_IP" != "null" ]]; then
    ok "Ingress IP: ${INGRESS_IP}"

    # Step 3: Wait for backends to become healthy (must have annotation and all HEALTHY)
    info "Waiting for load balancer health checks to pass (this can take 3-5 minutes)..."
    RETRIES=0
    MAX_RETRIES=40
    while true; do
      RETRIES=$((RETRIES + 1))
      if [[ "$RETRIES" -ge "$MAX_RETRIES" ]]; then
        warn "Health checks did not pass after ${MAX_RETRIES} attempts."
        info "Check manually: kubectl describe ingress -n ${K8S_NS}"
        break
      fi
      BACKENDS=$(kubectl get ingress -n "$K8S_NS" -o jsonpath='{.items[0].metadata.annotations.ingress\.kubernetes\.io/backends}' 2>/dev/null || echo "")
      if [[ -z "$BACKENDS" ]] || echo "$BACKENDS" | grep -q "Unknown\|UNHEALTHY"; then
        echo -n "."
        sleep 10
      else
        echo ""
        ok "All backends healthy!"
        break
      fi
    done

    # Step 4: Verify dashboard is reachable
    info "Verifying dashboard is reachable..."
    RETRIES=0
    MAX_RETRIES=18
    while true; do
      RETRIES=$((RETRIES + 1))
      if [[ "$RETRIES" -ge "$MAX_RETRIES" ]]; then
        warn "Dashboard not reachable after ${MAX_RETRIES} attempts."
        info "GCE load balancer may still be propagating. Try: curl http://${INGRESS_IP}"
        break
      fi
      HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" --connect-timeout 5 "http://${INGRESS_IP}/" 2>/dev/null || echo "000")
      if [[ "$HTTP_CODE" == "200" ]]; then
        echo ""
        ok "Dashboard is live!"
        break
      fi
      echo -n "."
      sleep 10
    done

    echo ""
    echo -e "  ${GREEN}${BOLD}━━━ Setup Complete ━━━${NC}"
    echo ""
    echo -e "  ${BOLD}Dashboard:${NC}  http://${INGRESS_IP}"
    echo -e "  ${BOLD}API:${NC}        http://decisionbox-api-service.${K8S_NS}:8080 (cluster-internal)"
    echo ""
  fi
else
  info "Skipped Helm deploy. Run manually with:"
  echo ""
  if [[ "$CLOUD" == "gcp" ]]; then
    echo -e "  ${BOLD}API:${NC}"
    echo -e "  helm upgrade --install decisionbox-api ${HELM_DIR} \\"
    echo -e "    -f ${HELM_DIR}/values.yaml \\"
    echo -e "    -f ${HELM_VALUES}"
  fi
  echo ""
  echo -e "  ${BOLD}Dashboard:${NC}"
  echo -e "  helm upgrade --install decisionbox-dashboard ${HELM_CHARTS_DIR}/decisionbox-dashboard \\"
  echo -e "    -f ${HELM_CHARTS_DIR}/decisionbox-dashboard/values.yaml"
fi
