#!/usr/bin/env bash
set -euo pipefail

# ══════════════════════════════════════════════════════════════════════════════
# DecisionBox Platform — Interactive Setup Wizard
# Configures cloud infrastructure, secrets, and deploys via Terraform + Helm.
#
# Usage: ./setup.sh [--help] [--dry-run]
# ══════════════════════════════════════════════════════════════════════════════

VERSION="1.2.0"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SETUP_START=$(date +%s)
DRY_RUN=false
RESUME=false
DESTROY=false
SPINNER_PID=""
GO_BACK=false
TOTAL_STEPS=9

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
      echo "  --resume       Resume from Helm deploy (skips Terraform, reloads config from tfvars)"
      echo "  --destroy      Tear down everything (Helm releases, K8s namespace, Terraform resources)"
      echo ""
      echo "This wizard will:"
      echo "  1. Check prerequisites (terraform, gcloud/aws, kubectl, helm)"
      echo "  2. Select cloud provider"
      echo "  3. Configure secrets"
      echo "  4. Configure cloud provider settings"
      echo "  5. Authenticate with cloud provider (user or service account)"
      echo "  6. Set up Terraform state backend"
      echo "  7. Review configuration"
      echo "  8. Generate Terraform variables and Helm values"
      echo "  9. Run terraform init, plan, apply + deploy via Helm"
      echo ""
      echo "Type 'back' at any prompt to return to the previous step."
      echo ""
      echo "Supported providers: GCP, AWS"
      exit 0
      ;;
    --dry-run)
      DRY_RUN=true
      ;;
    --resume)
      RESUME=true
      ;;
    --destroy)
      DESTROY=true
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

# ─── Prompt helpers (with "back" support) ────────────────────────────────────

# Returns 0 on success, sets GO_BACK=true and returns 1 if user types "back"
prompt() {
  local var_name="$1" prompt_text="$2" default="${3:-}"
  GO_BACK=false
  local back_hint="${DIM}(back)${NC}"
  if [[ -n "$default" ]]; then
    read -rp "$(echo -e "${CYAN}?${NC} ${prompt_text} ${DIM}[${default}]${NC} ${back_hint}: ")" value
    if [[ "$value" == "back" ]]; then GO_BACK=true; return 1; fi
    printf -v "$var_name" '%s' "${value:-$default}"
  else
    read -rp "$(echo -e "${CYAN}?${NC} ${prompt_text} ${back_hint}: ")" value
    if [[ "$value" == "back" ]]; then GO_BACK=true; return 1; fi
    while [[ -z "$value" ]]; do
      err "This field is required."
      read -rp "$(echo -e "${CYAN}?${NC} ${prompt_text} ${back_hint}: ")" value
      if [[ "$value" == "back" ]]; then GO_BACK=true; return 1; fi
    done
    printf -v "$var_name" '%s' "$value"
  fi
}

prompt_choice() {
  local var_name="$1" prompt_text="$2" default="${3:-}" options="$4"
  while true; do
    prompt "$var_name" "$prompt_text" "$default" || return 1
    local val="${!var_name}"
    if echo "$options" | grep -qw "$val"; then
      return 0
    fi
    err "Invalid choice: ${val}. Options: ${options}"
  done
}

prompt_number() {
  local var_name="$1" prompt_text="$2" default="${3:-}"
  while true; do
    prompt "$var_name" "$prompt_text" "$default" || return 1
    local val="${!var_name}"
    if [[ "$val" =~ ^[0-9]+$ ]]; then
      return 0
    fi
    err "Must be a number. Got: ${val}"
  done
}

prompt_boolean() {
  local var_name="$1" prompt_text="$2" default="${3:-false}"
  while true; do
    prompt "$var_name" "$prompt_text (true/false)" "$default" || return 1
    local val="${!var_name}"
    if [[ "$val" == "true" || "$val" == "false" ]]; then
      return 0
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

# ══════════════════════════════════════════════════════════════════════════════
# Step Functions
# ══════════════════════════════════════════════════════════════════════════════

do_step_1_prerequisites() {
  step_header 1 "$TOTAL_STEPS" "Prerequisites"

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
}

do_step_2_cloud_provider() {
  step_header 2 "$TOTAL_STEPS" "Cloud Provider"

  echo -e "  ${BOLD}1)${NC} GCP  — Google Cloud Platform"
  echo -e "  ${BOLD}2)${NC} AWS  — Amazon Web Services"
  echo ""
  prompt_choice CLOUD_CHOICE "Select cloud provider" "1" "1 2 gcp GCP aws AWS" || return 1

  case "$CLOUD_CHOICE" in
    1|gcp|GCP) CLOUD="gcp" ;;
    2|aws|AWS) CLOUD="aws" ;;
  esac

  CLOUD_UPPER="$(echo "$CLOUD" | tr '[:lower:]' '[:upper:]')"
  ok "Cloud provider: ${BOLD}${CLOUD_UPPER}${NC}"

  echo ""
  if [[ "$CLOUD" == "gcp" ]]; then
    check_tool "gcloud" "Install: https://cloud.google.com/sdk/docs/install" || {
      err "gcloud CLI is required for GCP. Install and re-run."
      exit 1
    }
  elif [[ "$CLOUD" == "aws" ]]; then
    check_tool "aws" "Install: https://docs.aws.amazon.com/cli/latest/userguide/getting-started-install.html" || {
      err "AWS CLI is required for AWS. Install and re-run."
      exit 1
    }
  fi
}

do_step_3_secrets() {
  step_header 3 "$TOTAL_STEPS" "Secrets Configuration"

  info "The secret namespace prefixes all secrets to avoid conflicts."
  dim "Format: {namespace}-{projectID}-{key} (e.g., decisionbox-proj123-llm-api-key)"
  echo ""
  prompt SECRET_NS "Secret namespace" "decisionbox" || return 1
  ok "Secret namespace: ${BOLD}${SECRET_NS}${NC}"

  echo ""
  CLOUD_UPPER="$(echo "$CLOUD" | tr '[:lower:]' '[:upper:]')"
  echo -e "  ${BOLD}1)${NC} Enable  — Use ${CLOUD_UPPER} Secret Manager ${DIM}(recommended for production)${NC}"
  echo -e "  ${BOLD}2)${NC} Disable — Use MongoDB encrypted secrets or K8s native secrets"
  echo ""
  prompt_choice SECRETS_CHOICE "Enable cloud secret manager?" "1" "1 2 yes y no n" || return 1

  case "$SECRETS_CHOICE" in
    1|yes|y) ENABLE_SECRETS="true" ;;
    2|no|n)  ENABLE_SECRETS="false" ;;
  esac

  ok "Cloud secret manager: ${BOLD}${ENABLE_SECRETS}${NC}"
}

do_step_4_provider_config() {
  if [[ "$CLOUD" == "gcp" ]]; then
    step_header 4 "$TOTAL_STEPS" "GCP Configuration"

    TF_DIR="${SCRIPT_DIR}/gcp/prod"

    prompt PROJECT_ID "GCP project ID" "${PROJECT_ID:-}" || return 1

    if [[ ! "$PROJECT_ID" =~ ^[a-z][a-z0-9-]{4,28}[a-z0-9]$ ]]; then
      warn "Project ID '${PROJECT_ID}' may not match GCP naming rules (lowercase, digits, hyphens, 6-30 chars)."
      dim "Continuing anyway — Terraform will validate against the API."
    fi

    prompt REGION "GCP region" "${REGION:-us-central1}" || return 1
    prompt CLUSTER_NAME "GKE cluster name" "${CLUSTER_NAME:-decisionbox-prod}" || return 1
    prompt K8S_NS "Kubernetes namespace" "${K8S_NS:-decisionbox}" || return 1

    echo ""
    info "Node pool configuration:"
    prompt MACHINE_TYPE "Machine type" "${MACHINE_TYPE:-e2-standard-2}" || return 1
    prompt_number MIN_NODES "Min nodes per zone" "${MIN_NODES:-1}" || return 1
    prompt_number MAX_NODES "Max nodes per zone" "${MAX_NODES:-2}" || return 1

    if [[ "$MIN_NODES" -gt "$MAX_NODES" ]]; then
      err "Min nodes (${MIN_NODES}) cannot be greater than max nodes (${MAX_NODES})."
      return 1
    fi

    echo ""
    prompt_boolean BQ_IAM "Enable BigQuery IAM for data warehouse access?" "${BQ_IAM:-false}" || return 1
    prompt_boolean VERTEX_AI_IAM "Enable Vertex AI IAM for LLM access (Claude via Vertex, Gemini)?" "${VERTEX_AI_IAM:-false}" || return 1

  elif [[ "$CLOUD" == "aws" ]]; then
    step_header 4 "$TOTAL_STEPS" "AWS Configuration"

    TF_DIR="${SCRIPT_DIR}/aws/prod"

    prompt AWS_REGION "AWS region" "${AWS_REGION:-us-east-1}" || return 1
    REGION="$AWS_REGION"
    prompt CLUSTER_NAME "EKS cluster name" "${CLUSTER_NAME:-decisionbox-prod}" || return 1
    prompt K8S_NS "Kubernetes namespace" "${K8S_NS:-decisionbox}" || return 1

    echo ""
    info "Node group configuration:"
    prompt INSTANCE_TYPE "Instance type" "${INSTANCE_TYPE:-t3.large}" || return 1
    prompt_number MIN_NODES "Min nodes" "${MIN_NODES:-1}" || return 1
    prompt_number MAX_NODES "Max nodes" "${MAX_NODES:-3}" || return 1
    prompt_number DESIRED_NODES "Desired nodes" "${DESIRED_NODES:-2}" || return 1

    if [[ "$MIN_NODES" -gt "$MAX_NODES" ]]; then
      err "Min nodes (${MIN_NODES}) cannot be greater than max nodes (${MAX_NODES})."
      return 1
    fi
    if [[ "$DESIRED_NODES" -lt "$MIN_NODES" || "$DESIRED_NODES" -gt "$MAX_NODES" ]]; then
      err "Desired nodes (${DESIRED_NODES}) must be between min (${MIN_NODES}) and max (${MAX_NODES})."
      return 1
    fi

    echo ""
    prompt_boolean BEDROCK_IAM "Enable Bedrock IAM for LLM access?" "${BEDROCK_IAM:-false}" || return 1
    prompt_boolean REDSHIFT_IAM "Enable Redshift IAM for data warehouse access?" "${REDSHIFT_IAM:-false}" || return 1
  fi
}

do_step_5_authentication() {
  if [[ "$CLOUD" == "aws" ]]; then
    step_header 5 "$TOTAL_STEPS" "AWS Authentication"

    info "Terraform needs AWS credentials. Choose how to authenticate:"
    echo ""
    echo -e "  ${BOLD}1)${NC} AWS CLI profile     — Use existing AWS CLI configuration"
    dim "     Best for: interactive setup, personal accounts"
    echo -e "  ${BOLD}2)${NC} Environment variables — Use AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY"
    dim "     Best for: CI/CD, automated pipelines"
    echo ""
    prompt_choice AWS_AUTH_CHOICE "Authentication method" "1" "1 2" || return 1

    if [[ "$AWS_AUTH_CHOICE" == "1" ]]; then
      prompt AWS_PROFILE "AWS CLI profile" "${AWS_PROFILE:-default}" || return 1
      export AWS_PROFILE="$AWS_PROFILE"
      ok "Using AWS profile: ${BOLD}${AWS_PROFILE}${NC}"
    else
      if [[ -n "${AWS_ACCESS_KEY_ID:-}" ]]; then
        ok "AWS_ACCESS_KEY_ID already set in environment"
      else
        prompt AWS_ACCESS_KEY_ID "AWS_ACCESS_KEY_ID" "" || return 1
        prompt AWS_SECRET_ACCESS_KEY "AWS_SECRET_ACCESS_KEY" "" || return 1
        export AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY
        ok "AWS credentials set for this session"
      fi
    fi

    # Verify identity
    echo ""
    spinner_start "Verifying AWS identity..."
    AWS_IDENTITY=$(aws sts get-caller-identity --output json 2>&1) && AWS_AUTH_RC=0 || AWS_AUTH_RC=$?
    spinner_stop

    if [[ "$AWS_AUTH_RC" -ne 0 ]]; then
      err "AWS authentication failed:"
      echo "$AWS_IDENTITY"
      return 1
    fi

    AWS_ACCOUNT_ID=$(echo "$AWS_IDENTITY" | grep -o '"Account"[[:space:]]*:[[:space:]]*"[^"]*"' | grep -o '"[^"]*"$' | tr -d '"')
    AWS_CALLER_ARN=$(echo "$AWS_IDENTITY" | grep -o '"Arn"[[:space:]]*:[[:space:]]*"[^"]*"' | grep -o '"[^"]*"$' | tr -d '"')
    ok "Authenticated as: ${DIM}${AWS_CALLER_ARN}${NC}"
    ok "Account ID: ${BOLD}${AWS_ACCOUNT_ID}${NC}"

    return 0
  fi

  if [[ "$CLOUD" != "gcp" ]]; then return 0; fi

  step_header 5 "$TOTAL_STEPS" "GCP Authentication"

  info "Terraform needs GCP credentials. Choose how to authenticate:"
  echo ""
  echo -e "  ${BOLD}1)${NC} User credentials  — Use your own Google account via ${BOLD}gcloud auth application-default login${NC}"
  dim "     Best for: interactive setup, personal projects, first-time setup"
  echo -e "  ${BOLD}2)${NC} Service account   — Use an existing service account key file"
  dim "     Best for: CI/CD, shared environments, automated pipelines"
  echo ""
  prompt_choice GCP_AUTH_CHOICE "Authentication method" "1" "1 2" || return 1

  if [[ "$GCP_AUTH_CHOICE" == "1" ]]; then
    # Check if ADC exists AND is a user credential (not a service account)
    local adc_needs_refresh=true
    if gcloud auth application-default print-access-token > /dev/null 2>&1; then
      local adc_file="${CLOUDSDK_CONFIG:-$HOME/.config/gcloud}/application_default_credentials.json"
      if [[ -f "$adc_file" ]]; then
        local adc_type
        adc_type=$(grep -o '"type"[[:space:]]*:[[:space:]]*"[^"]*"' "$adc_file" 2>/dev/null | head -1 | grep -o '"[^"]*"$' | tr -d '"' || echo "")
        if [[ "$adc_type" == "authorized_user" ]]; then
          ok "Application Default Credentials configured (user credentials)"
          prompt USE_EXISTING_ADC "Use existing credentials? (yes/no)" "yes" || return 1
          [[ "$USE_EXISTING_ADC" == "yes" ]] && adc_needs_refresh=false
        else
          warn "Application Default Credentials exist but use a service account, not user credentials."
          dim "Terraform will authenticate as the service account, which may lack permissions."
          info "Re-authenticating with your user account..."
        fi
      fi
    fi

    if [[ "$adc_needs_refresh" == "true" ]]; then
      info "Authenticate below — copy the URL, log in, and paste the code back here."
      echo ""
      gcloud auth application-default login --project="$PROJECT_ID" --no-browser 2>&1 || \
        gcloud auth application-default login --project="$PROJECT_ID"
      ok "Authenticated with user credentials"
    fi
  else
    prompt GCP_SA_KEY_FILE "Path to service account key file (JSON)" "${GCP_SA_KEY_FILE:-}" || return 1
    if [[ ! -f "$GCP_SA_KEY_FILE" ]]; then
      err "File not found: ${GCP_SA_KEY_FILE}"
      return 1
    fi
    export GOOGLE_APPLICATION_CREDENTIALS="$GCP_SA_KEY_FILE"
    ok "Using service account: ${GCP_SA_KEY_FILE}"
    dim "GOOGLE_APPLICATION_CREDENTIALS set for this session"
  fi

  # ─── Verify permissions ────────────────────────────────────────────
  echo ""
  spinner_start "Verifying GCP permissions..."
  PERM_ERRORS=0
  PERM_MISSING_GKE="" PERM_MISSING_STORAGE="" PERM_MISSING_IAM="" PERM_MISSING_COMPUTE=""

  gcloud container clusters list --project="$PROJECT_ID" --region="$REGION" > /dev/null 2>&1 || { PERM_ERRORS=$((PERM_ERRORS + 1)); PERM_MISSING_GKE=true; }
  gcloud storage buckets list --project="$PROJECT_ID" --limit=1 > /dev/null 2>&1 || { PERM_ERRORS=$((PERM_ERRORS + 1)); PERM_MISSING_STORAGE=true; }
  gcloud iam service-accounts list --project="$PROJECT_ID" --limit=1 > /dev/null 2>&1 || { PERM_ERRORS=$((PERM_ERRORS + 1)); PERM_MISSING_IAM=true; }
  gcloud compute networks list --project="$PROJECT_ID" --limit=1 > /dev/null 2>&1 || { PERM_ERRORS=$((PERM_ERRORS + 1)); PERM_MISSING_COMPUTE=true; }

  spinner_stop

  if [[ "$PERM_ERRORS" -gt 0 ]]; then
    warn "Permission issues detected (${PERM_ERRORS}):"
    [[ "$PERM_MISSING_GKE" == "true" ]] && err "  Missing: container.clusters.list (GKE access)"
    [[ "$PERM_MISSING_STORAGE" == "true" ]] && err "  Missing: storage.buckets.list (Terraform state)"
    [[ "$PERM_MISSING_IAM" == "true" ]] && err "  Missing: iam.serviceAccounts.list (Workload Identity)"
    [[ "$PERM_MISSING_COMPUTE" == "true" ]] && err "  Missing: compute.networks.list (VPC creation)"
    echo ""
    dim "The authenticated account needs Project Editor or Owner role."
    dim "Grant via: gcloud projects add-iam-policy-binding ${PROJECT_ID} \\"
    dim "  --member='user:YOUR_EMAIL' --role='roles/editor'"
    echo ""
    prompt CONTINUE_ANYWAY "Continue anyway? (yes/no)" "no" || return 1
    if [[ "$CONTINUE_ANYWAY" != "yes" ]]; then
      return 1
    fi
  else
    ok "All required permissions verified"
  fi
}

do_step_6_terraform_state() {
  step_header 6 "$TOTAL_STEPS" "Terraform State"

  if [[ "$CLOUD" == "gcp" ]]; then
    info "Terraform state must be stored in a GCS bucket for persistence and team collaboration."
    echo ""
    prompt TF_STATE_BUCKET "GCS bucket name" "${TF_STATE_BUCKET:-${PROJECT_ID}-terraform-state}" || return 1
    prompt TF_STATE_PREFIX "State prefix (environment)" "${TF_STATE_PREFIX:-prod}" || return 1

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
    info "Terraform state must be stored in an S3 bucket (uses S3-native locking)."
    echo ""
    prompt TF_STATE_BUCKET "S3 bucket name" "${TF_STATE_BUCKET:-${AWS_ACCOUNT_ID}-terraform-state}" || return 1
    prompt TF_STATE_KEY "State key" "${TF_STATE_KEY:-prod/terraform.tfstate}" || return 1

    if [[ "$DRY_RUN" == "false" ]]; then
      # S3 bucket
      if AWS_PAGER="" aws s3api head-bucket --bucket "$TF_STATE_BUCKET" > /dev/null 2>&1; then
        ok "Bucket s3://${TF_STATE_BUCKET} already exists"
      else
        spinner_start "Creating bucket s3://${TF_STATE_BUCKET}..."
        if [[ "$REGION" == "us-east-1" ]]; then
          aws s3api create-bucket --bucket "$TF_STATE_BUCKET" --region "$REGION" > /dev/null 2>&1
        else
          aws s3api create-bucket --bucket "$TF_STATE_BUCKET" --region "$REGION" \
            --create-bucket-configuration LocationConstraint="$REGION" > /dev/null 2>&1
        fi
        aws s3api put-bucket-versioning --bucket "$TF_STATE_BUCKET" \
          --versioning-configuration Status=Enabled > /dev/null 2>&1
        aws s3api put-public-access-block --bucket "$TF_STATE_BUCKET" \
          --public-access-block-configuration BlockPublicAcls=true,IgnorePublicAcls=true,BlockPublicPolicy=true,RestrictPublicBuckets=true > /dev/null 2>&1
        spinner_stop
        ok "Created bucket s3://${TF_STATE_BUCKET} with versioning"
      fi
    else
      dim "Dry-run: skipping bucket/table creation"
    fi
  fi
}

do_step_7_review() {
  step_header 7 "$TOTAL_STEPS" "Review Configuration"

  echo -e "  ${BOLD}Cloud:${NC}              $(echo "$CLOUD" | tr '[:lower:]' '[:upper:]')"
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
    echo -e "  ${BOLD}Vertex AI IAM:${NC}      ${VERTEX_AI_IAM}"
    echo -e "  ${BOLD}State bucket:${NC}       gs://${TF_STATE_BUCKET}/${TF_STATE_PREFIX}/"
  elif [[ "$CLOUD" == "aws" ]]; then
    echo -e "  ${BOLD}AWS account:${NC}        ${AWS_ACCOUNT_ID}"
    echo -e "  ${BOLD}Region:${NC}             ${REGION}"
    echo -e "  ${BOLD}Cluster:${NC}            ${CLUSTER_NAME}"
    echo -e "  ${BOLD}K8s namespace:${NC}      ${K8S_NS}"
    echo -e "  ${BOLD}Instance type:${NC}      ${INSTANCE_TYPE}"
    echo -e "  ${BOLD}Nodes:${NC}              ${MIN_NODES}-${MAX_NODES} (desired: ${DESIRED_NODES})"
    echo -e "  ${BOLD}Bedrock IAM:${NC}        ${BEDROCK_IAM}"
    echo -e "  ${BOLD}Redshift IAM:${NC}       ${REDSHIFT_IAM}"
    echo -e "  ${BOLD}State bucket:${NC}       s3://${TF_STATE_BUCKET}/${TF_STATE_KEY}"
  fi

  echo ""
  prompt CONFIRM "Proceed with this configuration? (yes/no/back)" "yes" || return 1

  if [[ "$CONFIRM" == "back" ]]; then
    GO_BACK=true
    return 1
  fi

  if [[ "$CONFIRM" != "yes" ]]; then
    warn "Setup cancelled. Re-run to start over."
    exit 0
  fi
}

do_step_8_generate() {
  step_header 8 "$TOTAL_STEPS" "Generate Config Files"

  if [[ "$CLOUD" == "gcp" ]]; then
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
enable_bigquery_iam  = ${BQ_IAM}
enable_vertex_ai_iam = ${VERTEX_AI_IAM}
EOF

    ok "Generated ${TFVARS_FILE}"

    HELM_DIR="${SCRIPT_DIR}/../helm-charts/decisionbox-api"
    HELM_VALUES="${HELM_DIR}/values-secrets.yaml"
    K8S_SA="decisionbox-api"
    K8S_AGENT_SA="decisionbox-agent"
    GCP_SA="${CLUSTER_NAME}-api@${PROJECT_ID}.iam.gserviceaccount.com"
    GCP_AGENT_SA="${CLUSTER_NAME}-agent@${PROJECT_ID}.iam.gserviceaccount.com"

    if [[ "$ENABLE_SECRETS" == "true" ]]; then
      cat > "$HELM_VALUES" <<EOF
# Generated by setup.sh v${VERSION} on $(date -u +"%Y-%m-%dT%H:%M:%SZ")

namespace: ${K8S_NS}

serviceAccountName: ${K8S_SA}
serviceAccountAnnotations:
  iam.gke.io/gcp-service-account: "${GCP_SA}"

agentServiceAccount:
  name: ${K8S_AGENT_SA}
  annotations:
    iam.gke.io/gcp-service-account: "${GCP_AGENT_SA}"

automountServiceAccountToken: true

extraEnvFrom:
  - secretRef:
      name: decisionbox-api-secrets

env:
  SECRET_PROVIDER: "gcp"
  SECRET_NAMESPACE: "${SECRET_NS}"
  SECRET_GCP_PROJECT_ID: "${PROJECT_ID}"
  AGENT_SERVICE_ACCOUNT: "${K8S_AGENT_SA}"
EOF
    else
      cat > "$HELM_VALUES" <<EOF
# Generated by setup.sh v${VERSION} on $(date -u +"%Y-%m-%dT%H:%M:%SZ")

namespace: ${K8S_NS}

serviceAccountName: ${K8S_SA}

agentServiceAccount:
  name: ${K8S_AGENT_SA}

extraEnvFrom:
  - secretRef:
      name: decisionbox-api-secrets

env:
  SECRET_PROVIDER: "mongodb"
  SECRET_NAMESPACE: "${SECRET_NS}"
  AGENT_SERVICE_ACCOUNT: "${K8S_AGENT_SA}"
EOF
    fi

    ok "Generated ${HELM_VALUES}"

  elif [[ "$CLOUD" == "aws" ]]; then
    TFVARS_FILE="${TF_DIR}/terraform.tfvars"

    cat > "$TFVARS_FILE" <<EOF
# Generated by setup.sh v${VERSION} on $(date -u +"%Y-%m-%dT%H:%M:%SZ")

region       = "${REGION}"
cluster_name = "${CLUSTER_NAME}"

# EKS node group
instance_type      = "${INSTANCE_TYPE}"
min_node_count     = ${MIN_NODES}
max_node_count     = ${MAX_NODES}
desired_node_count = ${DESIRED_NODES}

# IRSA
k8s_namespace = "${K8S_NS}"

# Optional features
enable_aws_secrets  = ${ENABLE_SECRETS}
secret_namespace    = "${SECRET_NS}"
enable_bedrock_iam  = ${BEDROCK_IAM}
enable_redshift_iam = ${REDSHIFT_IAM}
EOF

    ok "Generated ${TFVARS_FILE}"

    HELM_DIR="${SCRIPT_DIR}/../helm-charts/decisionbox-api"
    HELM_VALUES="${HELM_DIR}/values-secrets.yaml"
    K8S_SA="decisionbox-api"
    K8S_AGENT_SA="decisionbox-agent"
    IRSA_ROLE_ARN="arn:aws:iam::${AWS_ACCOUNT_ID}:role/${CLUSTER_NAME}-api"
    IRSA_AGENT_ROLE_ARN="arn:aws:iam::${AWS_ACCOUNT_ID}:role/${CLUSTER_NAME}-agent"

    if [[ "$ENABLE_SECRETS" == "true" ]]; then
      cat > "$HELM_VALUES" <<EOF
# Generated by setup.sh v${VERSION} on $(date -u +"%Y-%m-%dT%H:%M:%SZ")

namespace: ${K8S_NS}

serviceAccountName: ${K8S_SA}
serviceAccountAnnotations:
  eks.amazonaws.com/role-arn: "${IRSA_ROLE_ARN}"

agentServiceAccount:
  name: ${K8S_AGENT_SA}
  annotations:
    eks.amazonaws.com/role-arn: "${IRSA_AGENT_ROLE_ARN}"

automountServiceAccountToken: true

extraEnvFrom:
  - secretRef:
      name: decisionbox-api-secrets

env:
  SECRET_PROVIDER: "aws"
  SECRET_NAMESPACE: "${SECRET_NS}"
  SECRET_AWS_REGION: "${REGION}"
  AGENT_SERVICE_ACCOUNT: "${K8S_AGENT_SA}"
EOF
    else
      cat > "$HELM_VALUES" <<EOF
# Generated by setup.sh v${VERSION} on $(date -u +"%Y-%m-%dT%H:%M:%SZ")

namespace: ${K8S_NS}

serviceAccountName: ${K8S_SA}
serviceAccountAnnotations:
  eks.amazonaws.com/role-arn: "${IRSA_ROLE_ARN}"

agentServiceAccount:
  name: ${K8S_AGENT_SA}

extraEnvFrom:
  - secretRef:
      name: decisionbox-api-secrets

env:
  SECRET_PROVIDER: "mongodb"
  SECRET_NAMESPACE: "${SECRET_NS}"
  AGENT_SERVICE_ACCOUNT: "${K8S_AGENT_SA}"
EOF
    fi

    ok "Generated ${HELM_VALUES}"
  fi

  if [[ "$DRY_RUN" == "true" ]]; then
    echo ""
    ok "Dry-run complete. Config files generated. No infrastructure changes made."
    echo ""
    dim "To apply manually:"
    dim "  cd ${TF_DIR}"
    if [[ "$CLOUD" == "gcp" ]]; then
      dim "  terraform init -backend-config=\"bucket=${TF_STATE_BUCKET}\" -backend-config=\"prefix=${TF_STATE_PREFIX}\""
    elif [[ "$CLOUD" == "aws" ]]; then
      dim "  terraform init -backend-config=\"bucket=${TF_STATE_BUCKET}\" -backend-config=\"key=${TF_STATE_KEY}\" -backend-config=\"region=${REGION}\" -backend-config=\"use_lockfile=true\""
    fi
    dim "  terraform plan -out=tfplan"
    dim "  terraform apply tfplan"
    echo ""
    echo -e "  ${DIM}Total time: $(elapsed)${NC}"
    exit 0
  fi
}

build_helm_deps() {
  local chart_dir="$1"
  # Add Bitnami repo if not present
  if ! helm repo list 2>/dev/null | grep -q bitnami; then
    spinner_start "Adding Bitnami Helm repo..."
    helm repo add bitnami https://charts.bitnami.com/bitnami > /dev/null 2>&1
    spinner_stop
    ok "Added Bitnami Helm repo"
  fi

  spinner_start "Building Helm chart dependencies..."
  HELM_DEP_OUTPUT=$(helm dependency build "$chart_dir" 2>&1) && HELM_DEP_RC=0 || HELM_DEP_RC=$?
  spinner_stop
  if [[ "$HELM_DEP_RC" -ne 0 ]]; then
    err "Helm dependency build failed:"
    echo "$HELM_DEP_OUTPUT"
    exit 1
  fi
  ok "Chart dependencies ready"
}

do_helm_deploy() {
  HELM_CHARTS_DIR="${SCRIPT_DIR}/../helm-charts"
  DASH_DIR="${HELM_CHARTS_DIR}/decisionbox-dashboard"

  # Create namespace
  kubectl create namespace "$K8S_NS" --dry-run=client -o yaml | kubectl apply -f - > /dev/null 2>&1

  # Create API secrets
  API_SECRET_NAME="decisionbox-api-secrets"
  if kubectl get secret "$API_SECRET_NAME" -n "$K8S_NS" > /dev/null 2>&1; then
    ok "Secret ${API_SECRET_NAME} already exists"
  elif [[ "$ENABLE_SECRETS" == "true" ]]; then
    kubectl create secret generic "$API_SECRET_NAME" \
      -n "$K8S_NS" > /dev/null 2>&1
    ok "Created secret ${API_SECRET_NAME} ${DIM}(cloud secret manager — no encryption key needed)${NC}"
  else
    AUTO_KEY=$(openssl rand -base64 32)
    echo ""
    info "SECRET_ENCRYPTION_KEY is used for AES-256 encryption of secrets stored in MongoDB."
    dim "Press Enter to use the auto-generated key, or paste your own."
    prompt ENCRYPTION_KEY "SECRET_ENCRYPTION_KEY" "$AUTO_KEY"
    kubectl create secret generic "$API_SECRET_NAME" \
      --from-literal=SECRET_ENCRYPTION_KEY="$ENCRYPTION_KEY" \
      -n "$K8S_NS" > /dev/null 2>&1
    ok "Created secret ${API_SECRET_NAME} with SECRET_ENCRYPTION_KEY"
  fi

  echo ""
  prompt HELM_VALUES_ENV "Additional API values file (leave empty to skip)" "none"

  # Build chart dependencies
  echo ""
  build_helm_deps "$HELM_DIR"

  # Deploy API
  spinner_start "Deploying API..."
  HELM_ARGS=(helm upgrade --install decisionbox-api "$HELM_DIR" -n "$K8S_NS" --create-namespace -f "${HELM_DIR}/values.yaml")
  [[ -f "$HELM_VALUES" ]] && HELM_ARGS+=(-f "$HELM_VALUES")
  if [[ "$HELM_VALUES_ENV" != "none" && -n "$HELM_VALUES_ENV" ]]; then
    [[ ! "$HELM_VALUES_ENV" = /* ]] && HELM_VALUES_ENV="${SCRIPT_DIR}/../${HELM_VALUES_ENV}"
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
    echo ""
    warn "Fix the issue and re-run: ./setup.sh --resume"
    exit 1
  fi
  ok "API deployed"

  # Deploy Dashboard
  spinner_start "Deploying Dashboard..."
  DASH_ARGS=(helm upgrade --install decisionbox-dashboard "$DASH_DIR" -n "$K8S_NS" --create-namespace -f "${DASH_DIR}/values.yaml" --set "namespace=${K8S_NS}")
  if [[ "$CLOUD" == "aws" ]]; then
    DASH_ARGS+=(
      --set "ingress.ingressClassName=alb"
      --set "ingress.annotations.alb\.ingress\.kubernetes\.io/scheme=internet-facing"
      --set "ingress.annotations.alb\.ingress\.kubernetes\.io/target-type=ip"
    )
  fi
  DASH_OUTPUT=$("${DASH_ARGS[@]}" 2>&1) && DASH_RC=0 || DASH_RC=$?
  spinner_stop
  if [[ "$DASH_RC" -ne 0 ]]; then
    err "Dashboard deployment failed:"
    echo "$DASH_OUTPUT"
    echo ""
    warn "Fix the issue and re-run: ./setup.sh --resume"
    exit 1
  fi
  ok "Dashboard deployed"

  wait_for_ingress_and_show_result
}

wait_for_ingress_and_show_result() {
  # Wait for ingress
  echo ""
  info "Waiting for dashboard to become available..."
  echo ""

  spinner_start "Waiting for ingress resource..."
  INGRESS_ATTEMPTS=0
  while true; do
    RETRIES=0; INGRESS_FOUND=false
    while [[ "$RETRIES" -lt 12 ]]; do
      if kubectl get ingress -n "$K8S_NS" -o name 2>/dev/null | grep -q .; then
        INGRESS_FOUND=true; break
      fi
      RETRIES=$((RETRIES + 1)); sleep 5
    done
    if [[ "$INGRESS_FOUND" == "true" ]]; then
      sleep 10
      kubectl get ingress -n "$K8S_NS" -o name 2>/dev/null | grep -q . && break
    fi
    INGRESS_ATTEMPTS=$((INGRESS_ATTEMPTS + 1))
    if [[ "$INGRESS_ATTEMPTS" -ge 3 ]]; then
      spinner_stop; warn "Ingress not created after 3 attempts."
      dim "Check: kubectl get ingress -n ${K8S_NS}"
      break
    fi
    "${DASH_ARGS[@]}" > /dev/null 2>&1 || true
  done
  spinner_stop
  ok "Ingress resource exists"

  # Wait for IP/hostname
  spinner_start "Waiting for external address (1-3 min)..."
  RETRIES=0; INGRESS_ADDR=""
  while [[ -z "$INGRESS_ADDR" || "$INGRESS_ADDR" == "null" ]]; do
    RETRIES=$((RETRIES + 1))
    [[ "$RETRIES" -ge 30 ]] && { spinner_stop; warn "Address not assigned after 5 minutes."; break; }
    if ! kubectl get ingress -n "$K8S_NS" -o name 2>/dev/null | grep -q .; then
      "${DASH_ARGS[@]}" > /dev/null 2>&1 || true; sleep 15; continue
    fi
    # Try IP first (GCP), then hostname (AWS ALB)
    INGRESS_ADDR=$(kubectl get ingress -n "$K8S_NS" -o jsonpath='{.items[0].status.loadBalancer.ingress[0].ip}' 2>/dev/null || echo "")
    if [[ -z "$INGRESS_ADDR" || "$INGRESS_ADDR" == "null" ]]; then
      INGRESS_ADDR=$(kubectl get ingress -n "$K8S_NS" -o jsonpath='{.items[0].status.loadBalancer.ingress[0].hostname}' 2>/dev/null || echo "")
    fi
    [[ -z "$INGRESS_ADDR" || "$INGRESS_ADDR" == "null" ]] && sleep 10
  done
  spinner_stop

  if [[ -n "$INGRESS_ADDR" && "$INGRESS_ADDR" != "null" ]]; then
    ok "Ingress address: ${BOLD}${INGRESS_ADDR}${NC}"

    # Health checks (GCP-specific annotation check — skip for AWS ALB)
    if [[ "$CLOUD" != "aws" ]]; then
      spinner_start "Waiting for health checks (3-5 min)..."
      RETRIES=0
      while true; do
        RETRIES=$((RETRIES + 1))
        [[ "$RETRIES" -ge 40 ]] && { spinner_stop; warn "Health checks not passing."; break; }
        BACKENDS=$(kubectl get ingress -n "$K8S_NS" -o jsonpath='{.items[0].metadata.annotations.ingress\.kubernetes\.io/backends}' 2>/dev/null || echo "")
        if [[ -n "$BACKENDS" ]] && ! echo "$BACKENDS" | grep -q "Unknown\|UNHEALTHY"; then
          spinner_stop; ok "All backends healthy"; break
        fi
        sleep 10
      done
    fi

    # Verify HTTP 200
    spinner_start "Verifying dashboard is reachable..."
    RETRIES=0; DASHBOARD_LIVE=false
    while [[ "$RETRIES" -lt 18 ]]; do
      RETRIES=$((RETRIES + 1))
      HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" --connect-timeout 5 "http://${INGRESS_ADDR}/" 2>/dev/null || echo "000")
      [[ "$HTTP_CODE" == "200" ]] && { DASHBOARD_LIVE=true; break; }
      sleep 10
    done
    spinner_stop

    [[ "$DASHBOARD_LIVE" == "true" ]] && ok "Dashboard is live!" || warn "Dashboard not responding yet. Try: curl http://${INGRESS_ADDR}"

    echo ""
    echo -e "  ${GREEN}${BOLD}╔══════════════════════════════════════════════════╗${NC}"
    echo -e "  ${GREEN}${BOLD}║              Setup Complete!                     ║${NC}"
    echo -e "  ${GREEN}${BOLD}╚══════════════════════════════════════════════════╝${NC}"
    echo ""
    echo -e "  ${BOLD}Dashboard:${NC}  http://${INGRESS_ADDR}"
    echo -e "  ${BOLD}API:${NC}        http://decisionbox-api-service.${K8S_NS}:8080 ${DIM}(cluster-internal)${NC}"
    echo -e "  ${BOLD}Namespace:${NC}  ${K8S_NS}"
    echo ""
    echo -e "  ${DIM}Total time: $(elapsed)${NC}"
    echo ""
  else
    echo ""
    warn "Could not determine ingress address."
    dim "Check manually: kubectl get ingress -n ${K8S_NS}"
    echo ""
    echo -e "  ${DIM}Total time: $(elapsed)${NC}"
    echo ""
  fi
}

do_step_9_deploy() {
  step_header 9 "$TOTAL_STEPS" "Terraform & Deploy"

  cd "$TF_DIR"
  dim "Working directory: ${TF_DIR}"
  echo ""

  # ─── Terraform Init ────────────────────────────────────────────────
  spinner_start "Running terraform init..."
  if [[ "$CLOUD" == "gcp" ]]; then
    TF_INIT_ARGS=(-input=false -backend-config="bucket=${TF_STATE_BUCKET}" -backend-config="prefix=${TF_STATE_PREFIX}")
  elif [[ "$CLOUD" == "aws" ]]; then
    TF_INIT_ARGS=(-input=false -backend-config="bucket=${TF_STATE_BUCKET}" -backend-config="key=${TF_STATE_KEY}" -backend-config="region=${REGION}" -backend-config="use_lockfile=true")
  fi
  TF_INIT_OUTPUT=$(terraform init "${TF_INIT_ARGS[@]}" 2>&1) && TF_INIT_RC=0 || TF_INIT_RC=$?
  spinner_stop

  if [[ "$TF_INIT_RC" -ne 0 ]]; then
    err "Terraform init failed:"
    echo "$TF_INIT_OUTPUT"
    exit 1
  fi
  if [[ "$CLOUD" == "gcp" ]]; then
    ok "Terraform initialized ${DIM}(state: gs://${TF_STATE_BUCKET}/${TF_STATE_PREFIX}/)${NC}"
  elif [[ "$CLOUD" == "aws" ]]; then
    ok "Terraform initialized ${DIM}(state: s3://${TF_STATE_BUCKET}/${TF_STATE_KEY})${NC}"
  fi

  # ─── Terraform Plan ────────────────────────────────────────────────
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
    else
      info "Skipped apply. Run manually: cd ${TF_DIR} && terraform apply tfplan"
    fi
    rm -f tfplan
  fi

  # ─── Configure kubectl ─────────────────────────────────────────────
  echo ""
  if [[ "$CLOUD" == "gcp" ]]; then
    spinner_start "Fetching cluster credentials..."
    gcloud container clusters get-credentials "$CLUSTER_NAME" \
      --region "$REGION" \
      --project "$PROJECT_ID" 2>/dev/null
    spinner_stop
    ok "kubectl configured for ${CLUSTER_NAME}"
  elif [[ "$CLOUD" == "aws" ]]; then
    spinner_start "Fetching cluster credentials..."
    aws eks update-kubeconfig \
      --name "$CLUSTER_NAME" \
      --region "$REGION" > /dev/null 2>&1
    spinner_stop
    ok "kubectl configured for ${CLUSTER_NAME}"
  fi

  spinner_start "Waiting for Kubernetes API..."
  RETRIES=0
  until kubectl get nodes > /dev/null 2>&1; do
    RETRIES=$((RETRIES + 1))
    if [[ "$RETRIES" -ge 30 ]]; then
      spinner_stop
      err "Kubernetes API not reachable after 5 minutes."
      exit 1
    fi
    sleep 10
  done
  spinner_stop
  ok "Kubernetes API is ready"

  # ─── Set default StorageClass (AWS only) ────────────────────────────
  if [[ "$CLOUD" == "aws" ]]; then
    if kubectl get storageclass gp2 > /dev/null 2>&1; then
      kubectl patch storageclass gp2 -p '{"metadata": {"annotations":{"storageclass.kubernetes.io/is-default-class":"true"}}}' > /dev/null 2>&1
      ok "Set gp2 as default StorageClass"
    fi
  fi

  # ─── AWS Load Balancer Controller (AWS only) ─────────────────────────
  if [[ "$CLOUD" == "aws" ]]; then
    LB_ROLE_ARN=$(terraform -chdir="$TF_DIR" output -raw lb_controller_role_arn 2>/dev/null || echo "")
    if [[ -n "$LB_ROLE_ARN" ]]; then
      if helm list -n kube-system 2>/dev/null | grep -q aws-load-balancer-controller; then
        ok "AWS Load Balancer Controller already installed"
      else
        spinner_start "Installing AWS Load Balancer Controller..."
        helm repo add eks https://aws.github.io/eks-charts > /dev/null 2>&1
        helm repo update eks > /dev/null 2>&1
        helm install aws-load-balancer-controller eks/aws-load-balancer-controller \
          -n kube-system \
          --set clusterName="$CLUSTER_NAME" \
          --set serviceAccount.create=true \
          --set serviceAccount.name=aws-load-balancer-controller \
          --set serviceAccount.annotations."eks\.amazonaws\.com/role-arn"="$LB_ROLE_ARN" \
          --set region="$REGION" \
          --set vpcId="$(terraform -chdir="$TF_DIR" output -raw vpc_id 2>/dev/null)" > /dev/null 2>&1
        spinner_stop
        ok "Installed AWS Load Balancer Controller"
      fi
    fi
  fi

  # ─── Helm Deploy ───────────────────────────────────────────────────
  HELM_CHARTS_DIR="${SCRIPT_DIR}/../helm-charts"

  echo ""
  prompt HELM_DEPLOY "Deploy services via Helm? (yes/no)" "no"

  if [[ "$HELM_DEPLOY" == "yes" ]]; then
    do_helm_deploy
  else
    echo ""
    info "Skipped Helm deploy. To deploy manually:"
    echo ""
    echo -e "  ${BOLD}# API${NC}"
    echo -e "  ${DIM}helm upgrade --install decisionbox-api ${HELM_DIR} \\${NC}"
    echo -e "  ${DIM}  -f ${HELM_DIR}/values.yaml \\${NC}"
    echo -e "  ${DIM}  -f ${HELM_VALUES} -n ${K8S_NS}${NC}"
    echo ""
    echo -e "  ${BOLD}# Dashboard${NC}"
    echo -e "  ${DIM}helm upgrade --install decisionbox-dashboard ${HELM_CHARTS_DIR}/decisionbox-dashboard \\${NC}"
    echo -e "  ${DIM}  -f ${HELM_CHARTS_DIR}/decisionbox-dashboard/values.yaml -n ${K8S_NS}${NC}"
    echo ""
    echo -e "  ${DIM}Total time: $(elapsed)${NC}"
  fi
}

# ══════════════════════════════════════════════════════════════════════════════
# Main — Step Navigation with Back Support
# ══════════════════════════════════════════════════════════════════════════════

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

# ─── Destroy Mode ─────────────────────────────────────────────────────────

if [[ "$DESTROY" == "true" ]]; then
  if [[ "$DRY_RUN" == "true" ]]; then
    err "Cannot combine --destroy with --dry-run."
    exit 1
  fi

  warn "Destroy mode: this will tear down ALL DecisionBox infrastructure."
  echo ""

  # Detect which provider was used
  GCP_TFVARS="${SCRIPT_DIR}/gcp/prod/terraform.tfvars"
  AWS_TFVARS="${SCRIPT_DIR}/aws/prod/terraform.tfvars"

  if [[ -f "$GCP_TFVARS" && -f "$AWS_TFVARS" ]]; then
    echo -e "  ${BOLD}1)${NC} GCP  — ${GCP_TFVARS}"
    echo -e "  ${BOLD}2)${NC} AWS  — ${AWS_TFVARS}"
    echo ""
    prompt_choice DESTROY_CLOUD "Which deployment to destroy?" "1" "1 2 gcp aws"
    case "$DESTROY_CLOUD" in
      1|gcp) CLOUD="gcp" ;;
      2|aws) CLOUD="aws" ;;
    esac
  elif [[ -f "$GCP_TFVARS" ]]; then
    CLOUD="gcp"
  elif [[ -f "$AWS_TFVARS" ]]; then
    CLOUD="aws"
  else
    err "No terraform.tfvars found. Nothing to destroy."
    exit 1
  fi

  TFVARS_FILE="${SCRIPT_DIR}/${CLOUD}/prod/terraform.tfvars"
  TF_DIR="${SCRIPT_DIR}/${CLOUD}/prod"

  parse_tfvar() { grep "^${1}\s*=" "$TFVARS_FILE" 2>/dev/null | head -1 | sed 's/.*=\s*//; s/"//g; s/\s*$//' || true ; }

  REGION=$(parse_tfvar region)
  CLUSTER_NAME=$(parse_tfvar cluster_name)
  K8S_NS=$(parse_tfvar k8s_namespace)

  if [[ "$CLOUD" == "gcp" ]]; then
    PROJECT_ID=$(parse_tfvar project_id)
    ENABLE_SECRETS=$(parse_tfvar enable_gcp_secrets)
    BQ_IAM=$(parse_tfvar enable_bigquery_iam)
    VERTEX_AI_IAM=$(parse_tfvar enable_vertex_ai_iam)
    if [[ -z "$PROJECT_ID" || -z "$CLUSTER_NAME" ]]; then
      err "Failed to parse config from ${TFVARS_FILE}"
      exit 1
    fi
    echo -e "  ${BOLD}Provider:${NC}    GCP"
    echo -e "  ${BOLD}Project:${NC}     ${PROJECT_ID}"
    echo -e "  ${BOLD}Cluster:${NC}     ${CLUSTER_NAME}"
    echo -e "  ${BOLD}Region:${NC}      ${REGION}"
    echo -e "  ${BOLD}Namespace:${NC}   ${K8S_NS}"
    echo -e "  ${BOLD}Secrets:${NC}     ${ENABLE_SECRETS}"
    echo -e "  ${BOLD}BigQuery:${NC}    ${BQ_IAM}"
    echo -e "  ${BOLD}Vertex AI:${NC}   ${VERTEX_AI_IAM}"
  elif [[ "$CLOUD" == "aws" ]]; then
    ENABLE_SECRETS=$(parse_tfvar enable_aws_secrets)
    REDSHIFT_IAM=$(parse_tfvar enable_redshift_iam)
    BEDROCK_IAM=$(parse_tfvar enable_bedrock_iam)
    if [[ -z "$CLUSTER_NAME" ]]; then
      err "Failed to parse config from ${TFVARS_FILE}"
      exit 1
    fi
    echo -e "  ${BOLD}Provider:${NC}    AWS"
    echo -e "  ${BOLD}Cluster:${NC}     ${CLUSTER_NAME}"
    echo -e "  ${BOLD}Region:${NC}      ${REGION}"
    echo -e "  ${BOLD}Namespace:${NC}   ${K8S_NS}"
    echo -e "  ${BOLD}Secrets:${NC}     ${ENABLE_SECRETS}"
    echo -e "  ${BOLD}Redshift:${NC}    ${REDSHIFT_IAM}"
    echo -e "  ${BOLD}Bedrock:${NC}     ${BEDROCK_IAM}"
  fi
  echo ""

  prompt CONFIRM_DESTROY "Type 'destroy' to confirm teardown"
  if [[ "$CONFIRM_DESTROY" != "destroy" ]]; then
    info "Cancelled."
    exit 0
  fi

  # Check prerequisites
  do_step_1_prerequisites

  # Step 1: Uninstall Helm releases
  echo ""
  info "Uninstalling Helm releases..."

  CLUSTER_REACHABLE=false
  if [[ "$CLOUD" == "gcp" ]]; then
    gcloud container clusters get-credentials "$CLUSTER_NAME" --region "$REGION" --project "$PROJECT_ID" 2>/dev/null && CLUSTER_REACHABLE=true
  elif [[ "$CLOUD" == "aws" ]]; then
    aws eks update-kubeconfig --name "$CLUSTER_NAME" --region "$REGION" > /dev/null 2>&1 && CLUSTER_REACHABLE=true
  fi

  if [[ "$CLUSTER_REACHABLE" == "true" ]]; then
    if kubectl get ns "$K8S_NS" > /dev/null 2>&1; then
      spinner_start "Uninstalling decisionbox-dashboard..."
      helm uninstall decisionbox-dashboard -n "$K8S_NS" > /dev/null 2>&1 || true
      spinner_stop
      ok "Dashboard uninstalled"

      spinner_start "Uninstalling decisionbox-api..."
      helm uninstall decisionbox-api -n "$K8S_NS" > /dev/null 2>&1 || true
      spinner_stop
      ok "API uninstalled"

      spinner_start "Deleting namespace ${K8S_NS}..."
      kubectl delete namespace "$K8S_NS" --timeout=120s > /dev/null 2>&1 || true
      spinner_stop
      ok "Namespace deleted"
    else
      dim "Namespace ${K8S_NS} not found — skipping Helm cleanup"
    fi
  else
    dim "Cluster not reachable — skipping Helm cleanup"
  fi

  # Step 2: Terraform destroy
  echo ""
  info "Running terraform destroy..."
  cd "$TF_DIR"

  # Find state backend config
  spinner_start "Initializing Terraform..."
  if [[ "$CLOUD" == "gcp" ]]; then
    TF_STATE_BUCKET=$(grep 'bucket' .terraform/terraform.tfstate 2>/dev/null | head -1 | sed 's/.*"bucket":\s*"//; s/".*//' || echo "${PROJECT_ID}-terraform-state")
    TF_STATE_PREFIX=$(grep 'prefix' .terraform/terraform.tfstate 2>/dev/null | head -1 | sed 's/.*"prefix":\s*"//; s/".*//' || echo "prod")
    terraform init -input=false \
      -backend-config="bucket=${TF_STATE_BUCKET}" \
      -backend-config="prefix=${TF_STATE_PREFIX}" > /dev/null 2>&1 || {
      spinner_stop
      err "Terraform init failed. Run manually: cd ${TF_DIR} && terraform init"
      exit 1
    }
  elif [[ "$CLOUD" == "aws" ]]; then
    TF_STATE_BUCKET=$(grep 'bucket' .terraform/terraform.tfstate 2>/dev/null | head -1 | sed 's/.*"bucket":\s*"//; s/".*//' || echo "terraform-state")
    TF_STATE_KEY=$(grep '"key"' .terraform/terraform.tfstate 2>/dev/null | head -1 | sed 's/.*"key":\s*"//; s/".*//' || echo "prod/terraform.tfstate")
    terraform init -input=false \
      -backend-config="bucket=${TF_STATE_BUCKET}" \
      -backend-config="key=${TF_STATE_KEY}" \
      -backend-config="region=${REGION}" \
      -backend-config="use_lockfile=true" > /dev/null 2>&1 || {
      spinner_stop
      err "Terraform init failed. Run manually: cd ${TF_DIR} && terraform init"
      exit 1
    }
  fi
  spinner_stop
  ok "Terraform initialized"

  if [[ "$CLOUD" == "gcp" ]]; then
    echo ""
    info "Disabling deletion protection on GKE cluster (required before destroy)..."
    terraform apply -var="deletion_protection=false" -auto-approve > /dev/null 2>&1 || true
    ok "Deletion protection disabled"
  fi

  # Show destroy plan before applying
  echo ""
  info "Planning destruction..."
  echo ""
  terraform plan -destroy 2>&1
  echo ""

  prompt CONFIRM_APPLY_DESTROY "Proceed with destroying these resources? (yes/no)" "no"
  if [[ "$CONFIRM_APPLY_DESTROY" != "yes" ]]; then
    warn "Destroy cancelled. Resources are still running."
    if [[ "$CLOUD" == "gcp" ]]; then
      info "Deletion protection has been disabled — re-enable with: terraform apply -var=\"deletion_protection=true\""
    fi
    exit 0
  fi

  echo ""
  TF_DESTROY_START=$(date +%s)
  info "Destroying infrastructure (this may take 5-10 minutes)..."
  echo ""
  terraform destroy -auto-approve 2>&1
  TF_DESTROY_SECS=$(( $(date +%s) - TF_DESTROY_START ))
  echo ""

  echo -e "  ${RED}${BOLD}╔══════════════════════════════════════════════════╗${NC}"
  echo -e "  ${RED}${BOLD}║           Infrastructure Destroyed               ║${NC}"
  echo -e "  ${RED}${BOLD}╚══════════════════════════════════════════════════╝${NC}"
  echo ""
  echo -e "  ${BOLD}Cluster:${NC}     ${CLUSTER_NAME} ${DIM}(deleted)${NC}"
  if [[ "$CLOUD" == "gcp" ]]; then
    echo -e "  ${BOLD}Project:${NC}     ${PROJECT_ID}"
  elif [[ "$CLOUD" == "aws" ]]; then
    echo -e "  ${BOLD}Account:${NC}     ${AWS_ACCOUNT_ID}"
  fi
  echo ""
  echo -e "  ${DIM}Destroy time: ${TF_DESTROY_SECS}s${NC}"
  if [[ "$CLOUD" == "gcp" ]]; then
    echo -e "  ${DIM}State bucket gs://${TF_STATE_BUCKET} still exists (contains state history)${NC}"
  elif [[ "$CLOUD" == "aws" ]]; then
    echo -e "  ${DIM}State bucket s3://${TF_STATE_BUCKET} still exists (contains state history)${NC}"
  fi
  echo ""
  exit 0
fi

# ─── Resume Mode ──────────────────────────────────────────────────────────

if [[ "$RESUME" == "true" ]]; then
  info "Resume mode: loading config from previous run..."
  echo ""

  # Find tfvars — check both providers
  GCP_TFVARS="${SCRIPT_DIR}/gcp/prod/terraform.tfvars"
  AWS_TFVARS="${SCRIPT_DIR}/aws/prod/terraform.tfvars"

  if [[ -f "$GCP_TFVARS" && -f "$AWS_TFVARS" ]]; then
    echo -e "  ${BOLD}1)${NC} GCP  — ${GCP_TFVARS}"
    echo -e "  ${BOLD}2)${NC} AWS  — ${AWS_TFVARS}"
    echo ""
    prompt_choice RESUME_CLOUD "Which deployment to resume?" "1" "1 2 gcp aws"
    case "$RESUME_CLOUD" in
      1|gcp) CLOUD="gcp" ;;
      2|aws) CLOUD="aws" ;;
    esac
  elif [[ -f "$GCP_TFVARS" ]]; then
    CLOUD="gcp"
  elif [[ -f "$AWS_TFVARS" ]]; then
    CLOUD="aws"
  else
    err "No terraform.tfvars found. Run setup.sh without --resume first."
    exit 1
  fi

  TFVARS_FILE="${SCRIPT_DIR}/${CLOUD}/prod/terraform.tfvars"
  TF_DIR="${SCRIPT_DIR}/${CLOUD}/prod"

  # Parse tfvars (HCL key = "value" format)
  parse_tfvar() { grep "^${1}\s*=" "$TFVARS_FILE" 2>/dev/null | head -1 | sed 's/.*=\s*//; s/"//g; s/\s*$//' || true ; }

  REGION=$(parse_tfvar region)
  CLUSTER_NAME=$(parse_tfvar cluster_name)
  K8S_NS=$(parse_tfvar k8s_namespace)
  HELM_DIR="${SCRIPT_DIR}/../helm-charts/decisionbox-api"
  HELM_VALUES="${HELM_DIR}/values-secrets.yaml"

  if [[ "$CLOUD" == "gcp" ]]; then
    PROJECT_ID=$(parse_tfvar project_id)
    MACHINE_TYPE=$(parse_tfvar machine_type)
    MIN_NODES=$(parse_tfvar min_node_count)
    MAX_NODES=$(parse_tfvar max_node_count)
    ENABLE_SECRETS=$(parse_tfvar enable_gcp_secrets)
    SECRET_NS=$(parse_tfvar secret_namespace)
    BQ_IAM=$(parse_tfvar enable_bigquery_iam)
    VERTEX_AI_IAM=$(parse_tfvar enable_vertex_ai_iam)
    if [[ -z "$PROJECT_ID" || -z "$CLUSTER_NAME" || -z "$K8S_NS" ]]; then
      err "Failed to parse required values from ${TFVARS_FILE}"
      exit 1
    fi
  elif [[ "$CLOUD" == "aws" ]]; then
    INSTANCE_TYPE=$(parse_tfvar instance_type)
    MIN_NODES=$(parse_tfvar min_node_count)
    MAX_NODES=$(parse_tfvar max_node_count)
    DESIRED_NODES=$(parse_tfvar desired_node_count)
    ENABLE_SECRETS=$(parse_tfvar enable_aws_secrets)
    SECRET_NS=$(parse_tfvar secret_namespace)
    BEDROCK_IAM=$(parse_tfvar enable_bedrock_iam)
    REDSHIFT_IAM=$(parse_tfvar enable_redshift_iam)
    if [[ -z "$CLUSTER_NAME" || -z "$K8S_NS" ]]; then
      err "Failed to parse required values from ${TFVARS_FILE}"
      exit 1
    fi
  fi

  ok "Loaded config from ${TFVARS_FILE}"
  echo ""
  echo -e "  ${BOLD}Provider:${NC}    $(echo "$CLOUD" | tr '[:lower:]' '[:upper:]')"
  if [[ "$CLOUD" == "gcp" ]]; then
    echo -e "  ${BOLD}Project:${NC}     ${PROJECT_ID}"
  fi
  echo -e "  ${BOLD}Cluster:${NC}     ${CLUSTER_NAME}"
  echo -e "  ${BOLD}Region:${NC}      ${REGION}"
  echo -e "  ${BOLD}Namespace:${NC}   ${K8S_NS}"
  echo -e "  ${BOLD}Secrets:${NC}     ${ENABLE_SECRETS}"
  echo ""

  # Check prerequisites
  do_step_1_prerequisites

  # Validate cluster is reachable
  echo ""
  spinner_start "Verifying cluster connectivity..."

  # Ensure kubectl is configured
  if [[ "$CLOUD" == "gcp" ]]; then
    gcloud container clusters get-credentials "$CLUSTER_NAME" \
      --region "$REGION" \
      --project "$PROJECT_ID" 2>/dev/null || true
  elif [[ "$CLOUD" == "aws" ]]; then
    aws eks update-kubeconfig \
      --name "$CLUSTER_NAME" \
      --region "$REGION" > /dev/null 2>&1 || true
  fi

  if kubectl get nodes > /dev/null 2>&1; then
    spinner_stop
    ok "Cluster ${CLUSTER_NAME} is reachable"
  else
    spinner_stop
    err "Cannot reach cluster ${CLUSTER_NAME}."
    err "Ensure Terraform has been applied and the cluster is running."
    if [[ "$CLOUD" == "gcp" ]]; then
      dim "Check: gcloud container clusters list --project=${PROJECT_ID}"
    elif [[ "$CLOUD" == "aws" ]]; then
      dim "Check: aws eks list-clusters --region ${REGION}"
    fi
    exit 1
  fi

  # Validate Helm values file exists
  if [[ ! -f "$HELM_VALUES" ]]; then
    err "Helm values file not found: ${HELM_VALUES}"
    err "Run setup.sh without --resume to generate it."
    exit 1
  fi
  ok "Helm values file found: ${HELM_VALUES}"

  echo ""
  prompt CONFIRM_RESUME "Resume Helm deployment with this config? (yes/no)" "yes"
  if [[ "$CONFIRM_RESUME" != "yes" ]]; then
    warn "Cancelled. Run setup.sh without --resume to start fresh."
    exit 0
  fi

  # Jump to Helm deploy section
  HELM_CHARTS_DIR="${SCRIPT_DIR}/../helm-charts"
  DASH_DIR="${HELM_CHARTS_DIR}/decisionbox-dashboard"

  # Check if releases already exist
  API_DEPLOYED=false
  DASH_DEPLOYED=false
  helm status decisionbox-api -n "$K8S_NS" > /dev/null 2>&1 && API_DEPLOYED=true
  helm status decisionbox-dashboard -n "$K8S_NS" > /dev/null 2>&1 && DASH_DEPLOYED=true

  if [[ "$API_DEPLOYED" == "true" && "$DASH_DEPLOYED" == "true" ]]; then
    ok "API release already deployed"
    ok "Dashboard release already deployed"
    echo ""
    prompt REDEPLOY "Both releases exist. Re-deploy anyway? (yes/no)" "no"
    if [[ "$REDEPLOY" != "yes" ]]; then
      info "Skipping deploy. Checking ingress..."
      DASH_ARGS=(helm upgrade --install decisionbox-dashboard "$DASH_DIR" -n "$K8S_NS" --create-namespace -f "${DASH_DIR}/values.yaml" --set "namespace=${K8S_NS}")
      if [[ "$CLOUD" == "aws" ]]; then
        DASH_ARGS+=(
          --set "ingress.ingressClassName=alb"
          --set "ingress.annotations.alb\.ingress\.kubernetes\.io/scheme=internet-facing"
          --set "ingress.annotations.alb\.ingress\.kubernetes\.io/target-type=ip"
        )
      fi
      wait_for_ingress_and_show_result
      exit 0
    fi
  elif [[ "$API_DEPLOYED" == "true" ]]; then
    ok "API release already deployed"
  elif [[ "$DASH_DEPLOYED" == "true" ]]; then
    ok "Dashboard release already deployed"
  fi

  do_helm_deploy
  exit 0
fi

# ─── Normal Flow ──────────────────────────────────────────────────────────

dim "Type 'back' at any prompt to return to the previous step."

# Steps 1 is not navigable (prerequisites must pass).
# Steps 2-7 support "back" navigation.
# Steps 8-9 are sequential (no going back after generation/deploy).

do_step_1_prerequisites

CURRENT_STEP=2

while [[ "$CURRENT_STEP" -le 7 ]]; do
  STEP_RC=0
  case "$CURRENT_STEP" in
    2) do_step_2_cloud_provider || STEP_RC=$? ;;
    3) do_step_3_secrets || STEP_RC=$? ;;
    4) do_step_4_provider_config || STEP_RC=$? ;;
    5) do_step_5_authentication || STEP_RC=$? ;;
    6) do_step_6_terraform_state || STEP_RC=$? ;;
    7) do_step_7_review || STEP_RC=$? ;;
  esac

  if [[ "$GO_BACK" == "true" ]]; then
    GO_BACK=false
    if [[ "$CURRENT_STEP" -gt 2 ]]; then
      CURRENT_STEP=$((CURRENT_STEP - 1))
    else
      info "Already at the first configurable step."
    fi
  else
    CURRENT_STEP=$((CURRENT_STEP + 1))
  fi
done

do_step_8_generate
do_step_9_deploy
