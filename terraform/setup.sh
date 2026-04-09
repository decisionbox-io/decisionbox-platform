#!/usr/bin/env bash
set -euo pipefail

# ══════════════════════════════════════════════════════════════════════════════
# DecisionBox Platform — Interactive Setup Wizard
# Configures cloud infrastructure, secrets, and deploys via Terraform + Helm.
#
# Usage: ./setup.sh [--help] [--dry-run]
# ══════════════════════════════════════════════════════════════════════════════

VERSION="1.4.0"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SETUP_START=$(date +%s)
DRY_RUN=false
RESUME=false
DESTROY=false
SPINNER_PID=""
GO_BACK=false
TOTAL_STEPS=10
INCLUDE_FILES=()

# Multi-deployment support: project name, environment, and base directory
# can be set via CLI flags or interactively during the wizard.
CLI_PROJECT=""
CLI_ENV=""
CLI_BASE=""
CLI_PROVIDER=""

# ─── Plugin step registry ───────────────────────────────────────────────────
# Plugins register extra steps via register_step(). Steps are inserted before
# the review step. Each step is a function name + title.

PLUGIN_STEPS=()
PLUGIN_STEP_TITLES=()

# register_step registers an additional wizard step.
# Usage: register_step <function_name> <title>
# The function must accept no arguments and follow the same conventions
# as built-in steps (use prompt helpers, set GO_BACK=true to go back).
register_step() {
  PLUGIN_STEPS+=("$1")
  PLUGIN_STEP_TITLES+=("$2")
}

# ─── Parse arguments ─────────────────────────────────────────────────────────

for arg in "$@"; do
  case "$arg" in
    --help|-h)
      echo "DecisionBox Platform Setup Wizard v${VERSION}"
      echo ""
      echo "Usage: ./setup.sh [OPTIONS]"
      echo ""
      echo "Options:"
      echo "  --help, -h       Show this help message"
      echo "  --dry-run        Generate config files only (no terraform apply, no helm deploy)"
      echo "  --resume         Resume from Helm deploy (skips Terraform, reloads config from tfvars)"
      echo "  --destroy        Tear down everything (Helm releases, K8s namespace, Terraform resources)"
      echo "  --include FILE   Source a plugin script that registers additional steps via register_step()"
      echo "  --project NAME   Project name (default: decisionbox)"
      echo "  --env ENV        Environment: prod, staging, dev, or custom (default: prod)"
      echo "  --base DIR       Base directory for deployment files (default: this script's directory)"
      echo "  --provider CLOUD Cloud provider: gcp, aws, or azure (for --resume/--destroy)"
      echo ""
      echo "This wizard will:"
      echo "  1. Check prerequisites (terraform, gcloud/aws/az, kubectl, helm)"
      echo "  2. Set project name, environment, and deployment directory"
      echo "  3. Select cloud provider"
      echo "  4. Configure secrets"
      echo "  5. Configure cloud provider settings"
      echo "  6. Configure vector search (Qdrant)"
      echo "  7. Authenticate with cloud provider (user or service account)"
      echo "  8. Set up Terraform state backend"
      echo "  9. Review configuration"
      echo " 10. Generate Terraform variables and Helm values"
      echo " 11. Run terraform init, plan, apply + deploy via Helm"
      echo ""
      echo "Type 'back' at any prompt to return to the previous step."
      echo ""
      echo "Supported providers: GCP, AWS, Azure"
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
    --include)
      NEXT_IS_INCLUDE=true
      ;;
    --project)
      NEXT_IS_PROJECT=true
      ;;
    --env)
      NEXT_IS_ENV=true
      ;;
    --base)
      NEXT_IS_BASE=true
      ;;
    --provider)
      NEXT_IS_PROVIDER=true
      ;;
    *)
      if [[ "${NEXT_IS_INCLUDE:-}" == "true" ]]; then
        INCLUDE_FILES+=("$arg")
        NEXT_IS_INCLUDE=false
      elif [[ "${NEXT_IS_PROJECT:-}" == "true" ]]; then
        CLI_PROJECT="$arg"
        NEXT_IS_PROJECT=false
      elif [[ "${NEXT_IS_ENV:-}" == "true" ]]; then
        CLI_ENV="$arg"
        NEXT_IS_ENV=false
      elif [[ "${NEXT_IS_BASE:-}" == "true" ]]; then
        CLI_BASE="$arg"
        NEXT_IS_BASE=false
      elif [[ "${NEXT_IS_PROVIDER:-}" == "true" ]]; then
        CLI_PROVIDER="$arg"
        NEXT_IS_PROVIDER=false
      else
        echo "Unknown argument: $arg"
        echo "Run ./setup.sh --help for usage."
        exit 1
      fi
      ;;
  esac
done

# Validate CLI flag values (catch --project without a value, etc.)
for pending_var in NEXT_IS_INCLUDE NEXT_IS_PROJECT NEXT_IS_ENV NEXT_IS_BASE NEXT_IS_PROVIDER; do
  if [[ "${!pending_var:-}" == "true" ]]; then
    flag_name="${pending_var#NEXT_IS_}"
    echo "Error: --${flag_name,,} requires a value."
    exit 1
  fi
done

# Validate --provider value if provided
if [[ -n "$CLI_PROVIDER" && "$CLI_PROVIDER" != "gcp" && "$CLI_PROVIDER" != "aws" && "$CLI_PROVIDER" != "azure" ]]; then
  echo "Error: --provider must be 'gcp', 'aws', or 'azure', got '${CLI_PROVIDER}'"
  exit 1
fi

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

prompt_optional() {
  local var_name="$1" prompt_text="$2" default="${3:-}"
  GO_BACK=false
  local back_hint="${DIM}(back)${NC}"
  read -rp "$(echo -e "${CYAN}?${NC} ${prompt_text} ${DIM}[${default}]${NC} ${back_hint}: ")" value
  if [[ "$value" == "back" ]]; then GO_BACK=true; return 1; fi
  printf -v "$var_name" '%s' "${value:-$default}"
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

prompt_secret() {
  local var_name="$1" prompt_text="$2"
  GO_BACK=false
  local back_hint="${DIM}(back)${NC}"
  local value
  read -rs -p "$(echo -e "${CYAN}?${NC} ${prompt_text} ${back_hint}: ")" value
  echo ""
  if [[ "$value" == "back" ]]; then GO_BACK=true; return 1; fi
  while [[ -z "$value" ]]; do
    err "This field is required."
    read -rs -p "$(echo -e "${CYAN}?${NC} ${prompt_text} ${back_hint}: ")" value
    echo ""
    if [[ "$value" == "back" ]]; then GO_BACK=true; return 1; fi
  done
  printf -v "$var_name" '%s' "$value"
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

# ─── CIDR / IP helpers ───────────────────────────────────────────────────────

validate_cidr() {
  local cidr="$1"
  # Accept bare IP (no prefix) — caller is responsible for appending /32
  if [[ "$cidr" =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}(/[0-9]{1,2})?$ ]]; then
    local IFS='./'; read -ra parts <<< "$cidr"
    for i in 0 1 2 3; do
      [[ "${parts[$i]}" -gt 255 ]] && return 1
    done
    # If prefix is present, validate range
    if [[ "${#parts[@]}" -eq 5 ]]; then
      [[ "${parts[4]}" -gt 32 ]] && return 1
    fi
    return 0
  fi
  return 1
}

prompt_cidr_list() {
  local var_name="$1" prompt_text="$2" default="${3:-}"

  echo ""
  info "$prompt_text"
  dim "Enter IP addresses or CIDR blocks one per line. Press Enter on an empty line when done."
  dim "Examples: 62.56.81.243 (single IP) or 203.0.113.0/24 (range)"
  if [[ -n "$default" ]]; then
    dim "Current: ${default//,/, }"
    dim "Press Enter on the first line to keep current values, or type 'clear' to remove all."
  fi
  echo ""

  local cidrs=()
  local first_line=true
  while true; do
    local input
    read -rp "$(echo -e "${CYAN}+${NC} CIDR block ${DIM}(empty to finish)${NC}: ")" input
    if [[ "$input" == "back" ]]; then
      GO_BACK=true
      return 1
    fi
    if [[ -z "$input" ]]; then
      if $first_line && [[ -n "$default" ]]; then
        printf -v "$var_name" '%s' "$default"
        return 0
      fi
      break
    fi
    if [[ "$input" == "clear" ]]; then
      cidrs=()
      ok "Cleared all IP ranges (unrestricted access)"
      break
    fi
    first_line=false
    if validate_cidr "$input"; then
      # Auto-append /32 for bare IP addresses
      if [[ ! "$input" == */* ]]; then
        input="${input}/32"
      fi
      cidrs+=("$input")
      ok "Added: $input"
    else
      err "Invalid format: $input (expected: x.x.x.x or x.x.x.x/y)"
    fi
  done

  local result
  result=$(IFS=','; echo "${cidrs[*]}")
  printf -v "$var_name" '%s' "$result"
}

prompt_ip_restriction() {
  echo ""
  prompt_boolean ENABLE_IP_RESTRICTION "Restrict HTTP/HTTPS access to specific IP ranges?" "${ENABLE_IP_RESTRICTION:-false}" || return 1
  ALLOWED_IP_RANGES="${ALLOWED_IP_RANGES:-}"
  if [[ "$ENABLE_IP_RESTRICTION" == "true" ]]; then
    prompt_cidr_list ALLOWED_IP_RANGES "IP allowlisting" "${ALLOWED_IP_RANGES}" || return 1
    if [[ -z "$ALLOWED_IP_RANGES" ]]; then
      warn "No CIDR blocks entered. HTTP/HTTPS access will be unrestricted."
      ENABLE_IP_RESTRICTION="false"
    fi
  else
    ALLOWED_IP_RANGES=""
  fi
}

display_ip_restriction() {
  if [[ -n "${ALLOWED_IP_RANGES:-}" ]]; then
    echo -e "  ${BOLD}IP allowlist:${NC}       ${ALLOWED_IP_RANGES//,/, }"
  else
    echo -e "  ${BOLD}IP allowlist:${NC}       ${DIM}unrestricted${NC}"
  fi
}

csv_to_hcl_list() {
  local csv="$1"
  if [[ -z "$csv" ]]; then
    echo "[]"
    return
  fi
  local result="["
  local first=true
  IFS=',' read -ra items <<< "$csv"
  for item in "${items[@]}"; do
    if $first; then
      result+="\"${item}\""
      first=false
    else
      result+=", \"${item}\""
    fi
  done
  result+="]"
  echo "$result"
}

parse_tfvar_list() {
  local raw
  raw=$(grep -E "^${1}[[:space:]]*=" "$TFVARS_FILE" 2>/dev/null | head -1 | sed 's/.*=[[:space:]]*//' || true)
  echo "$raw" | tr -d '[]"[:space:]'
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

# ─── Deployment discovery (shared by --resume and --destroy) ────────────────
# Sets: PROJECT_NAME, ENVIRONMENT, CLOUD, DEPLOY_BASE, TF_DIR, TFVARS_FILE

resolve_deployment_dir() {
  local action_label="$1"  # "resume" or "destroy"

  PROJECT_NAME="${CLI_PROJECT:-}"
  ENVIRONMENT="${CLI_ENV:-}"
  CLOUD="${CLI_PROVIDER:-}"
  DEPLOY_BASE="${CLI_BASE:-${SCRIPT_DIR}}"
  DEPLOY_BASE="${DEPLOY_BASE/#\~/$HOME}"

  if [[ -z "$PROJECT_NAME" ]]; then
    prompt PROJECT_NAME "Project name" "decisionbox"
  fi
  if [[ -z "$ENVIRONMENT" ]]; then
    prompt ENVIRONMENT "Environment" "prod"
  fi

  if [[ -z "$CLOUD" ]]; then
    local gcp_tfvars="${DEPLOY_BASE}/${PROJECT_NAME}/gcp/${ENVIRONMENT}/terraform.tfvars"
    local aws_tfvars="${DEPLOY_BASE}/${PROJECT_NAME}/aws/${ENVIRONMENT}/terraform.tfvars"
    local azure_tfvars="${DEPLOY_BASE}/${PROJECT_NAME}/azure/${ENVIRONMENT}/terraform.tfvars"

    # Also check flat directory structure (e.g., terraform/gcp/prod/terraform.tfvars)
    local gcp_tfvars_flat="${DEPLOY_BASE}/gcp/${ENVIRONMENT}/terraform.tfvars"
    local aws_tfvars_flat="${DEPLOY_BASE}/aws/${ENVIRONMENT}/terraform.tfvars"
    local azure_tfvars_flat="${DEPLOY_BASE}/azure/${ENVIRONMENT}/terraform.tfvars"

    local found_providers=()
    [[ -f "$gcp_tfvars" || -f "$gcp_tfvars_flat" ]] && found_providers+=("gcp")
    [[ -f "$aws_tfvars" || -f "$aws_tfvars_flat" ]] && found_providers+=("aws")
    [[ -f "$azure_tfvars" || -f "$azure_tfvars_flat" ]] && found_providers+=("azure")

    if [[ ${#found_providers[@]} -eq 0 ]]; then
      err "No terraform.tfvars found at ${DEPLOY_BASE}/${PROJECT_NAME}/{gcp,aws,azure}/${ENVIRONMENT}/."
      dim "Specify --project, --env, and --base if using a custom location."
      exit 1
    elif [[ ${#found_providers[@]} -eq 1 ]]; then
      CLOUD="${found_providers[0]}"
    else
      local idx=1
      local valid_choices=""
      for p in "${found_providers[@]}"; do
        local p_upper
        p_upper=$(echo "$p" | tr '[:lower:]' '[:upper:]')
        echo -e "  ${BOLD}${idx})${NC} ${p_upper}  — ${DEPLOY_BASE}/${PROJECT_NAME}/${p}/${ENVIRONMENT}/terraform.tfvars"
        valid_choices+="${idx} ${p} "
        idx=$((idx + 1))
      done
      echo ""
      local cloud_choice
      prompt_choice cloud_choice "Which deployment to ${action_label}?" "1" "$valid_choices"
      if [[ "$cloud_choice" =~ ^[0-9]+$ ]]; then
        CLOUD="${found_providers[$((cloud_choice - 1))]}"
      else
        CLOUD="$cloud_choice"
      fi
    fi
  fi

  TF_DIR="${DEPLOY_BASE}/${PROJECT_NAME}/${CLOUD}/${ENVIRONMENT}"
  TFVARS_FILE="${TF_DIR}/terraform.tfvars"

  # Fall back to flat directory structure if nested path doesn't exist
  if [[ ! -f "$TFVARS_FILE" ]]; then
    local flat_dir="${DEPLOY_BASE}/${CLOUD}/${ENVIRONMENT}"
    if [[ -f "${flat_dir}/terraform.tfvars" ]]; then
      TF_DIR="$flat_dir"
      TFVARS_FILE="${TF_DIR}/terraform.tfvars"
    fi
  fi

  if [[ ! -f "$TFVARS_FILE" ]]; then
    err "No terraform.tfvars found at ${TFVARS_FILE}"
    exit 1
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

# ─── Kubeconfig helper ────────────────────────────────────────────────────────
# Ensure ~/.kube/config is in KUBECONFIG so kubectl can see contexts written
# by cloud CLIs (az aks get-credentials writes there by default).
ensure_default_kubeconfig() {
  local default_kc="$HOME/.kube/config"
  if [[ -f "$default_kc" ]] && [[ ":${KUBECONFIG:-}:" != *":${default_kc}:"* ]]; then
    export KUBECONFIG="${KUBECONFIG:+${KUBECONFIG}:}${default_kc}"
  fi
}

# Get the kubeconfig file to write Azure credentials into.
# Uses the first writable file in KUBECONFIG, falling back to ~/.kube/config.
azure_kubeconfig_file() {
  if [[ -n "${KUBECONFIG:-}" ]]; then
    local IFS=':'
    for f in $KUBECONFIG; do
      [[ -n "$f" && -w "$f" ]] && { echo "$f"; return; }
    done
    # No writable file found — check for config-files dir pattern
    local config_dir="$HOME/.kube/config-files"
    if [[ -d "$config_dir" ]]; then
      echo "${config_dir}/${CLUSTER_NAME}-azure"
      return
    fi
  fi
  echo "$HOME/.kube/config"
}

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

do_step_2_deployment() {
  step_header 2 "$TOTAL_STEPS" "Deployment Identity"

  info "Each deployment gets its own directory, state, and configuration."
  dim "Structure: {base}/{project}/{cloud}/{environment}/"
  echo ""

  prompt PROJECT_NAME "Project name" "${PROJECT_NAME:-${CLI_PROJECT:-decisionbox}}" || return 1
  # Validate: lowercase alphanumeric + hyphens
  if [[ ! "$PROJECT_NAME" =~ ^[a-z][a-z0-9-]*$ ]]; then
    err "Project name must be lowercase alphanumeric with hyphens (e.g., my-project)."
    PROJECT_NAME=""
    return 1
  fi
  ok "Project: ${BOLD}${PROJECT_NAME}${NC}"

  echo ""
  prompt ENVIRONMENT "Environment (prod, staging, dev, or custom)" "${ENVIRONMENT:-${CLI_ENV:-prod}}" || return 1
  if [[ ! "$ENVIRONMENT" =~ ^[a-z][a-z0-9-]*$ ]]; then
    err "Environment must be lowercase alphanumeric with hyphens."
    ENVIRONMENT=""
    return 1
  fi
  ok "Environment: ${BOLD}${ENVIRONMENT}${NC}"

  echo ""
  info "Base directory for deployment files."
  dim "Default keeps files inside the repo. Set a custom path for external deployments."
  prompt DEPLOY_BASE "Base directory" "${DEPLOY_BASE:-${CLI_BASE:-${SCRIPT_DIR}}}" || return 1
  # Expand ~ if present
  DEPLOY_BASE="${DEPLOY_BASE/#\~/$HOME}"
  # Resolve to absolute path
  DEPLOY_BASE="$(cd "$DEPLOY_BASE" 2>/dev/null && pwd || echo "$DEPLOY_BASE")"
  ok "Base directory: ${BOLD}${DEPLOY_BASE}${NC}"
}

do_step_3_cloud_provider() {
  step_header 3 "$TOTAL_STEPS" "Cloud Provider"

  echo -e "  ${BOLD}1)${NC} GCP   — Google Cloud Platform"
  echo -e "  ${BOLD}2)${NC} AWS   — Amazon Web Services"
  echo -e "  ${BOLD}3)${NC} Azure — Microsoft Azure"
  echo ""
  prompt_choice CLOUD_CHOICE "Select cloud provider" "1" "1 2 3 gcp GCP aws AWS azure Azure AZURE" || return 1

  case "$CLOUD_CHOICE" in
    1|gcp|GCP) CLOUD="gcp" ;;
    2|aws|AWS) CLOUD="aws" ;;
    3|azure|Azure|AZURE) CLOUD="azure" ;;
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
  elif [[ "$CLOUD" == "azure" ]]; then
    check_tool "az" "Install: https://learn.microsoft.com/en-us/cli/azure/install-azure-cli" || {
      err "Azure CLI is required for Azure. Install and re-run."
      exit 1
    }
  fi
}

do_step_4_secrets() {
  step_header 4 "$TOTAL_STEPS" "Secrets Configuration"

  info "The secret namespace prefixes all secrets to avoid conflicts."
  dim "Format: {namespace}-{projectID}-{key} (e.g., decisionbox-proj123-llm-api-key)"
  echo ""
  prompt SECRET_NS "Secret namespace" "decisionbox" || return 1
  ok "Secret namespace: ${BOLD}${SECRET_NS}${NC}"

  echo ""
  CLOUD_UPPER="$(echo "$CLOUD" | tr '[:lower:]' '[:upper:]')"
  local secrets_service="${CLOUD_UPPER} Secret Manager"
  [[ "$CLOUD" == "azure" ]] && secrets_service="Azure Key Vault"
  echo -e "  ${BOLD}1)${NC} Enable  — Use ${secrets_service} ${DIM}(recommended for production)${NC}"
  echo -e "  ${BOLD}2)${NC} Disable — Use MongoDB encrypted secrets or K8s native secrets"
  echo ""
  prompt_choice SECRETS_CHOICE "Enable cloud secret manager?" "1" "1 2 yes y no n" || return 1

  case "$SECRETS_CHOICE" in
    1|yes|y) ENABLE_SECRETS="true" ;;
    2|no|n)  ENABLE_SECRETS="false" ;;
  esac

  ok "Cloud secret manager: ${BOLD}${ENABLE_SECRETS}${NC}"
}

do_step_5_provider_config() {
  # Compute TF_DIR from project/cloud/env collected in step 2
  TF_DIR="${DEPLOY_BASE}/${PROJECT_NAME}/${CLOUD}/${ENVIRONMENT}"

  if [[ "$CLOUD" == "gcp" ]]; then
    step_header 5 "$TOTAL_STEPS" "GCP Configuration"

    prompt PROJECT_ID "GCP project ID" "${PROJECT_ID:-}" || return 1

    if [[ ! "$PROJECT_ID" =~ ^[a-z][a-z0-9-]{4,28}[a-z0-9]$ ]]; then
      warn "Project ID '${PROJECT_ID}' may not match GCP naming rules (lowercase, digits, hyphens, 6-30 chars)."
      dim "Continuing anyway — Terraform will validate against the API."
    fi

    prompt REGION "GCP region" "${REGION:-us-central1}" || return 1
    prompt CLUSTER_NAME "GKE cluster name" "${CLUSTER_NAME:-${PROJECT_NAME}-${ENVIRONMENT}}" || return 1
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

    prompt_ip_restriction || return 1

  elif [[ "$CLOUD" == "aws" ]]; then
    step_header 5 "$TOTAL_STEPS" "AWS Configuration"

    prompt AWS_REGION "AWS region" "${AWS_REGION:-us-east-1}" || return 1
    REGION="$AWS_REGION"
    prompt CLUSTER_NAME "EKS cluster name" "${CLUSTER_NAME:-${PROJECT_NAME}-${ENVIRONMENT}}" || return 1
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

    prompt_ip_restriction || return 1

  elif [[ "$CLOUD" == "azure" ]]; then
    step_header 5 "$TOTAL_STEPS" "Azure Configuration"

    TF_DIR="${SCRIPT_DIR}/azure/prod"

    prompt SUBSCRIPTION_ID "Azure subscription ID" "${SUBSCRIPTION_ID:-}" || return 1
    prompt LOCATION "Azure region (location)" "${LOCATION:-eastus}" || return 1
    REGION="$LOCATION"
    prompt CLUSTER_NAME "AKS cluster name" "${CLUSTER_NAME:-decisionbox-prod}" || return 1
    prompt AZURE_RG "Resource group name" "${AZURE_RG:-${CLUSTER_NAME}-rg}" || return 1
    prompt K8S_NS "Kubernetes namespace" "${K8S_NS:-decisionbox}" || return 1

    echo ""
    info "Node pool configuration:"
    prompt VM_SIZE "VM size" "${VM_SIZE:-Standard_D2s_v5}" || return 1
    prompt_number MIN_NODES "Min nodes" "${MIN_NODES:-3}" || return 1
    prompt_number MAX_NODES "Max nodes" "${MAX_NODES:-3}" || return 1

    if [[ "$MIN_NODES" -gt "$MAX_NODES" ]]; then
      err "Min nodes (${MIN_NODES}) cannot be greater than max nodes (${MAX_NODES})."
      return 1
    fi

    # Key Vault toggle follows the cloud secret manager choice from step 3
    ENABLE_KEY_VAULT="$ENABLE_SECRETS"

    prompt_ip_restriction || return 1
  fi
}

do_step_6_vector_search() {
  step_header 6 "$TOTAL_STEPS" "Vector Search (Qdrant)"

  info "Qdrant enables semantic search and AI-powered discovery recommendations."
  dim "If you don't need vector search, you can skip this step."
  echo ""

  prompt_boolean ENABLE_QDRANT "Enable vector search (Qdrant)?" "${ENABLE_QDRANT:-false}" || return 1

  if [[ "$ENABLE_QDRANT" == "true" ]]; then
    prompt QDRANT_URL "Qdrant gRPC endpoint" "${QDRANT_URL:-qdrant:6334}" || return 1
    prompt_optional QDRANT_API_KEY "Qdrant API key (optional, press Enter to skip)" "${QDRANT_API_KEY:-}"
    if [[ "$GO_BACK" == "true" ]]; then return 1; fi
    ok "Vector search: ${BOLD}enabled${NC} (${QDRANT_URL})"
  else
    QDRANT_URL=""
    QDRANT_API_KEY=""
    ok "Vector search: ${BOLD}disabled${NC}"
  fi
}

do_step_6_vector_search_review() {
  echo -e "  ${BOLD}Vector search:${NC}      ${ENABLE_QDRANT:-false}"
  if [[ "${ENABLE_QDRANT:-false}" == "true" ]]; then
    echo -e "  ${BOLD}Qdrant URL:${NC}         ${QDRANT_URL}"
    if [[ -n "${QDRANT_API_KEY:-}" ]]; then
      echo -e "  ${BOLD}Qdrant API key:${NC}     ${DIM}(set)${NC}"
    fi
  fi
}

do_step_7_authentication() {
  if [[ "$CLOUD" == "azure" ]]; then
    step_header 7 "$TOTAL_STEPS" "Azure Authentication"

    info "Terraform needs Azure credentials. Choose how to authenticate:"
    echo ""
    echo -e "  ${BOLD}1)${NC} Azure CLI login    — Use your Azure account via ${BOLD}az login${NC}"
    dim "     Best for: interactive setup, personal subscriptions"
    echo -e "  ${BOLD}2)${NC} Service principal  — Use an existing service principal"
    dim "     Best for: CI/CD, automated pipelines"
    echo ""
    prompt_choice AZURE_AUTH_CHOICE "Authentication method" "1" "1 2" || return 1

    if [[ "$AZURE_AUTH_CHOICE" == "1" ]]; then
      # Check if already logged in
      if az account show > /dev/null 2>&1; then
        local current_sub
        current_sub=$(az account show --query "id" -o tsv 2>/dev/null)
        if [[ "$current_sub" == "$SUBSCRIPTION_ID" ]]; then
          ok "Already logged in to subscription ${SUBSCRIPTION_ID}"
          prompt USE_EXISTING_AZ "Use existing credentials? (yes/no)" "yes" || return 1
          if [[ "$USE_EXISTING_AZ" != "yes" ]]; then
            az login > /dev/null 2>&1
          fi
        else
          info "Currently logged in to subscription ${current_sub}, switching..."
          az account set --subscription "$SUBSCRIPTION_ID" 2>/dev/null || {
            warn "Failed to switch subscription. Logging in again..."
            az login > /dev/null 2>&1
            az account set --subscription "$SUBSCRIPTION_ID"
          }
        fi
      else
        info "Opening browser for Azure login..."
        az login > /dev/null 2>&1
        az account set --subscription "$SUBSCRIPTION_ID"
      fi
      ok "Authenticated with Azure CLI"
    else
      prompt AZURE_TENANT_ID "Azure tenant ID" "${AZURE_TENANT_ID:-}" || return 1
      prompt AZURE_CLIENT_ID "Service principal client ID" "${AZURE_CLIENT_ID:-}" || return 1
      prompt_secret AZURE_CLIENT_SECRET "Service principal client secret" || return 1
      export ARM_TENANT_ID="$AZURE_TENANT_ID"
      export ARM_CLIENT_ID="$AZURE_CLIENT_ID"
      export ARM_CLIENT_SECRET="$AZURE_CLIENT_SECRET"
      export ARM_SUBSCRIPTION_ID="$SUBSCRIPTION_ID"
      ok "Service principal credentials set for this session"
    fi

    # Verify identity
    echo ""
    spinner_start "Verifying Azure identity..."
    AZURE_ACCOUNT=$(az account show --subscription "$SUBSCRIPTION_ID" -o json 2>&1) && AZURE_AUTH_RC=0 || AZURE_AUTH_RC=$?
    spinner_stop

    if [[ "$AZURE_AUTH_RC" -ne 0 ]]; then
      err "Azure authentication failed:"
      echo "$AZURE_ACCOUNT"
      return 1
    fi

    AZURE_SUB_NAME=$(echo "$AZURE_ACCOUNT" | grep -o '"name"[[:space:]]*:[[:space:]]*"[^"]*"' | head -1 | grep -o '"[^"]*"$' | tr -d '"')
    AZURE_TENANT=$(echo "$AZURE_ACCOUNT" | grep -o '"tenantId"[[:space:]]*:[[:space:]]*"[^"]*"' | grep -o '"[^"]*"$' | tr -d '"')
    ok "Subscription: ${BOLD}${AZURE_SUB_NAME}${NC} (${SUBSCRIPTION_ID})"
    ok "Tenant: ${DIM}${AZURE_TENANT}${NC}"

    return 0
  fi

  if [[ "$CLOUD" == "aws" ]]; then
    step_header 7 "$TOTAL_STEPS" "AWS Authentication"

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

  step_header 7 "$TOTAL_STEPS" "GCP Authentication"

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

do_step_8_terraform_state() {
  step_header 8 "$TOTAL_STEPS" "Terraform State"

  if [[ "$CLOUD" == "gcp" ]]; then
    info "Terraform state must be stored in a GCS bucket for persistence and team collaboration."
    echo ""
    prompt TF_STATE_BUCKET "GCS bucket name" "${TF_STATE_BUCKET:-${PROJECT_ID}-terraform-state}" || return 1
    prompt TF_STATE_PREFIX "State prefix" "${TF_STATE_PREFIX:-${PROJECT_NAME}/${ENVIRONMENT}}" || return 1

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
    prompt TF_STATE_KEY "State key" "${TF_STATE_KEY:-${PROJECT_NAME}/${ENVIRONMENT}/terraform.tfstate}" || return 1

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

  elif [[ "$CLOUD" == "azure" ]]; then
    info "Terraform state must be stored in an Azure Storage Account for persistence and team collaboration."
    echo ""
    prompt TF_STATE_RG "Resource group for Terraform state" "${TF_STATE_RG:-decisionbox-tfstate-rg}" || return 1
    # Azure storage account: 3-24 chars, lowercase + digits only, globally unique
    local sa_default
    sa_default=$(echo "${CLUSTER_NAME}tfstate" | sed 's/[-_]//g' | tr '[:upper:]' '[:lower:]' | cut -c1-24)
    prompt TF_STATE_SA "Storage account name (3-24 chars, lowercase, no hyphens)" "${TF_STATE_SA:-$sa_default}" || return 1
    prompt TF_STATE_CONTAINER "Container name" "${TF_STATE_CONTAINER:-tfstate}" || return 1
    prompt TF_STATE_KEY "State key" "${TF_STATE_KEY:-prod.terraform.tfstate}" || return 1

    if [[ "$DRY_RUN" == "false" ]]; then
      # Resource group
      if az group show --name "$TF_STATE_RG" > /dev/null 2>&1; then
        ok "Resource group ${TF_STATE_RG} already exists"
      else
        spinner_start "Creating resource group ${TF_STATE_RG}..."
        az group create --name "$TF_STATE_RG" --location "$LOCATION" > /dev/null 2>&1
        spinner_stop
        ok "Created resource group ${TF_STATE_RG}"
      fi

      # Storage account
      if az storage account show --name "$TF_STATE_SA" --resource-group "$TF_STATE_RG" > /dev/null 2>&1; then
        ok "Storage account ${TF_STATE_SA} already exists"
      else
        spinner_start "Creating storage account ${TF_STATE_SA}..."
        az storage account create \
          --name "$TF_STATE_SA" \
          --resource-group "$TF_STATE_RG" \
          --location "$LOCATION" \
          --sku Standard_LRS \
          --min-tls-version TLS1_2 \
          --allow-blob-public-access false > /dev/null 2>&1
        spinner_stop
        ok "Created storage account ${TF_STATE_SA}"
      fi

      # Container
      if az storage container show --name "$TF_STATE_CONTAINER" --account-name "$TF_STATE_SA" > /dev/null 2>&1; then
        ok "Container ${TF_STATE_CONTAINER} already exists"
      else
        spinner_start "Creating container ${TF_STATE_CONTAINER}..."
        az storage container create \
          --name "$TF_STATE_CONTAINER" \
          --account-name "$TF_STATE_SA" > /dev/null 2>&1
        spinner_stop
        ok "Created container ${TF_STATE_CONTAINER}"
      fi
    else
      dim "Dry-run: skipping storage account creation"
    fi
  fi
}

do_step_9_review() {
  step_header 9 "$TOTAL_STEPS" "Review Configuration"

  echo -e "  ${BOLD}Project:${NC}            ${PROJECT_NAME}"
  echo -e "  ${BOLD}Environment:${NC}        ${ENVIRONMENT}"
  echo -e "  ${BOLD}Deploy dir:${NC}         ${TF_DIR}"
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
    display_ip_restriction
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
    display_ip_restriction
  elif [[ "$CLOUD" == "azure" ]]; then
    echo -e "  ${BOLD}Subscription:${NC}       ${SUBSCRIPTION_ID}"
    echo -e "  ${BOLD}Location:${NC}           ${LOCATION}"
    echo -e "  ${BOLD}Cluster:${NC}            ${CLUSTER_NAME}"
    echo -e "  ${BOLD}Resource group:${NC}     ${AZURE_RG}"
    echo -e "  ${BOLD}K8s namespace:${NC}      ${K8S_NS}"
    echo -e "  ${BOLD}VM size:${NC}            ${VM_SIZE}"
    echo -e "  ${BOLD}Nodes:${NC}              ${MIN_NODES}-${MAX_NODES}"
    echo -e "  ${BOLD}Key Vault:${NC}          ${ENABLE_KEY_VAULT}"
    echo -e "  ${BOLD}State:${NC}              ${TF_STATE_SA}/${TF_STATE_CONTAINER}/${TF_STATE_KEY}"
    display_ip_restriction
  fi

  # Vector search
  echo ""
  do_step_6_vector_search_review

  # Show plugin review sections (plugins define <step_fn>_review)
  for fn in ${PLUGIN_STEPS[@]+"${PLUGIN_STEPS[@]}"}; do
    local review_fn="${fn}_review"
    if declare -f "$review_fn" > /dev/null 2>&1; then
      echo ""
      "$review_fn"
    fi
  done

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

do_step_10_generate() {
  step_header 10 "$TOTAL_STEPS" "Generate Config Files"

  # ─── Scaffold deployment directory if it doesn't exist ───────────
  if [[ ! -d "$TF_DIR" ]]; then
    info "Creating deployment directory: ${TF_DIR}"
    mkdir -p "$TF_DIR"

    # Copy template files from the repo's canonical template directory.
    # Always uses prod/ as the template regardless of target env — the env
    # difference is in terraform.tfvars (generated below), not in the HCL files.
    TEMPLATE_DIR="${SCRIPT_DIR}/${CLOUD}/prod"
    for f in main.tf variables.tf outputs.tf versions.tf; do
      if [[ -f "${TEMPLATE_DIR}/${f}" ]]; then
        cp "${TEMPLATE_DIR}/${f}" "${TF_DIR}/${f}"
      fi
    done

    # Compute the correct module source path.
    # If TF_DIR is inside the repo, use a relative path. Otherwise, use absolute.
    MODULE_DIR="${SCRIPT_DIR}/${CLOUD}/modules/decisionbox"
    MODULE_ABS="$(cd "$MODULE_DIR" && pwd)"

    # Check if relative path works by testing common prefix
    DEPLOY_ABS="$(cd "$TF_DIR" && pwd)"
    REPO_ABS="$(cd "$SCRIPT_DIR/.." && pwd)"

    if [[ "$DEPLOY_ABS" == "$REPO_ABS"/* ]]; then
      # Inside the repo — compute relative path from TF_DIR to modules (pure bash)
      # Walk up from DEPLOY_ABS to REPO_ABS counting levels, then append the module subpath
      local remaining="${DEPLOY_ABS#$REPO_ABS/}"
      local depth
      depth=$(echo "$remaining" | tr -cd '/' | wc -c)
      local ups=""
      for ((i = 0; i <= depth; i++)); do ups+="../"; done
      REL_PATH="${ups}terraform/${CLOUD}/modules/decisionbox"
      sed -i.bak "s|source[[:space:]]*=[[:space:]]*\"../modules/decisionbox\"|source = \"${REL_PATH}\"|g" "${TF_DIR}/main.tf" && rm -f "${TF_DIR}/main.tf.bak"
    else
      # Outside the repo — use absolute path to modules
      sed -i.bak "s|source[[:space:]]*=[[:space:]]*\"../modules/decisionbox\"|source = \"${MODULE_ABS}\"|g" "${TF_DIR}/main.tf" && rm -f "${TF_DIR}/main.tf.bak"
    fi

    ok "Scaffolded ${TF_DIR} from template"
  else
    dim "Deployment directory already exists: ${TF_DIR}"
  fi

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

# IP restriction
allowed_ip_ranges = $(csv_to_hcl_list "${ALLOWED_IP_RANGES:-}")
EOF

    ok "Generated ${TFVARS_FILE}"

    HELM_DIR="${SCRIPT_DIR}/../helm-charts/decisionbox-api"
    HELM_VALUES="${TF_DIR}/values-secrets.yaml"
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

qdrant:
  enabled: ${ENABLE_QDRANT:-false}
  url: "${QDRANT_URL:-}"
  apiKey: "${QDRANT_API_KEY:-}"
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

qdrant:
  enabled: ${ENABLE_QDRANT:-false}
  url: "${QDRANT_URL:-}"
  apiKey: "${QDRANT_API_KEY:-}"
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

# IP restriction
allowed_ip_ranges = $(csv_to_hcl_list "${ALLOWED_IP_RANGES:-}")
EOF

    ok "Generated ${TFVARS_FILE}"

    HELM_DIR="${SCRIPT_DIR}/../helm-charts/decisionbox-api"
    HELM_VALUES="${TF_DIR}/values-secrets.yaml"
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

qdrant:
  enabled: ${ENABLE_QDRANT:-false}
  url: "${QDRANT_URL:-}"
  apiKey: "${QDRANT_API_KEY:-}"
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

qdrant:
  enabled: ${ENABLE_QDRANT:-false}
  url: "${QDRANT_URL:-}"
  apiKey: "${QDRANT_API_KEY:-}"
EOF
    fi

    ok "Generated ${HELM_VALUES}"

  elif [[ "$CLOUD" == "azure" ]]; then
    TFVARS_FILE="${TF_DIR}/terraform.tfvars"

    cat > "$TFVARS_FILE" <<EOF
# Generated by setup.sh v${VERSION} on $(date -u +"%Y-%m-%dT%H:%M:%SZ")

subscription_id     = "${SUBSCRIPTION_ID}"
location            = "${LOCATION}"
cluster_name        = "${CLUSTER_NAME}"
resource_group_name = "${AZURE_RG}"

# AKS node pool
vm_size        = "${VM_SIZE}"
min_node_count = ${MIN_NODES}
max_node_count = ${MAX_NODES}

# Workload Identity
k8s_namespace = "${K8S_NS}"

# Optional features
enable_key_vault    = ${ENABLE_KEY_VAULT}
secret_namespace    = "${SECRET_NS}"
allowed_ip_ranges   = $(csv_to_hcl_list "${ALLOWED_IP_RANGES:-}")
EOF

    ok "Generated ${TFVARS_FILE}"

    HELM_DIR="${SCRIPT_DIR}/../helm-charts/decisionbox-api"
    HELM_VALUES="${HELM_DIR}/values-secrets.yaml"
    K8S_SA="decisionbox-api"
    K8S_AGENT_SA="decisionbox-agent"

    # Client IDs will be filled after terraform apply (from outputs)
    AZURE_API_CLIENT_ID="\${API_CLIENT_ID}"
    AZURE_AGENT_CLIENT_ID="\${AGENT_CLIENT_ID}"

    if [[ "$ENABLE_KEY_VAULT" == "true" ]]; then
      cat > "$HELM_VALUES" <<EOF
# Generated by setup.sh v${VERSION} on $(date -u +"%Y-%m-%dT%H:%M:%SZ")
# NOTE: After terraform apply, update client IDs from terraform output:
#   terraform -chdir=${TF_DIR} output api_identity_client_id
#   terraform -chdir=${TF_DIR} output agent_identity_client_id

namespace: ${K8S_NS}

serviceAccountName: ${K8S_SA}
serviceAccountAnnotations:
  azure.workload.identity/client-id: "${AZURE_API_CLIENT_ID}"
serviceAccountLabels:
  azure.workload.identity/use: "true"

agentServiceAccount:
  name: ${K8S_AGENT_SA}
  annotations:
    azure.workload.identity/client-id: "${AZURE_AGENT_CLIENT_ID}"
  labels:
    azure.workload.identity/use: "true"

podLabels:
  azure.workload.identity/use: "true"

automountServiceAccountToken: true

extraEnvFrom:
  - secretRef:
      name: decisionbox-api-secrets

env:
  SECRET_PROVIDER: "azure"
  SECRET_NAMESPACE: "${SECRET_NS}"
  AGENT_SERVICE_ACCOUNT: "${K8S_AGENT_SA}"

qdrant:
  enabled: ${ENABLE_QDRANT:-false}
  url: "${QDRANT_URL:-}"
  apiKey: "${QDRANT_API_KEY:-}"
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

qdrant:
  enabled: ${ENABLE_QDRANT:-false}
  url: "${QDRANT_URL:-}"
  apiKey: "${QDRANT_API_KEY:-}"
EOF
    fi

    ok "Generated ${HELM_VALUES}"
  fi

  # Run plugin generate hooks (plugins define <step_fn>_generate)
  for fn in ${PLUGIN_STEPS[@]+"${PLUGIN_STEPS[@]}"}; do
    local gen_fn="${fn}_generate"
    if declare -f "$gen_fn" > /dev/null 2>&1; then
      "$gen_fn"
    fi
  done

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
    elif [[ "$CLOUD" == "azure" ]]; then
      dim "  terraform init -backend-config=\"resource_group_name=${TF_STATE_RG}\" -backend-config=\"storage_account_name=${TF_STATE_SA}\" -backend-config=\"container_name=${TF_STATE_CONTAINER}\" -backend-config=\"key=${TF_STATE_KEY}\""
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

  # Add Qdrant repo if not present
  if ! helm repo list 2>/dev/null | grep -q qdrant; then
    spinner_start "Adding Qdrant Helm repo..."
    helm repo add qdrant https://qdrant.github.io/qdrant-helm > /dev/null 2>&1
    spinner_stop
    ok "Added Qdrant Helm repo"
  fi

  spinner_start "Updating Helm chart dependencies..."
  HELM_DEP_OUTPUT=$(helm dependency update "$chart_dir" 2>&1) && HELM_DEP_RC=0 || HELM_DEP_RC=$?
  spinner_stop
  if [[ "$HELM_DEP_RC" -ne 0 ]]; then
    err "Helm dependency update failed:"
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
  if [[ "$CLOUD" == "gcp" ]]; then
    # Attach Cloud Armor security policy via BackendConfig if IP restriction is configured
    ARMOR_POLICY=$(terraform -chdir="$TF_DIR" output -raw security_policy_name 2>/dev/null || echo "")
    if [[ -n "$ARMOR_POLICY" ]]; then
      DASH_ARGS+=(
        --set "cloudArmor.enabled=true"
        --set "cloudArmor.securityPolicy=${ARMOR_POLICY}"
      )
    fi
  elif [[ "$CLOUD" == "aws" ]]; then
    DASH_ARGS+=(
      --set "ingress.ingressClassName=alb"
      --set "ingress.annotations.alb\.ingress\.kubernetes\.io/scheme=internet-facing"
      --set "ingress.annotations.alb\.ingress\.kubernetes\.io/target-type=ip"
    )
    # Restrict ALB inbound traffic to allowed CIDRs if IP restriction is configured.
    # Uses inbound-cidrs (not security-groups) so the controller keeps managing
    # its own backend SG rules for ALB-to-pod connectivity.
    if [[ -n "${ALLOWED_IP_RANGES:-}" ]]; then
      DASH_ARGS+=(--set "ingress.annotations.alb\.ingress\.kubernetes\.io/inbound-cidrs=${ALLOWED_IP_RANGES}")
    fi
  elif [[ "$CLOUD" == "azure" ]]; then
    DASH_ARGS+=(
      --set "ingress.ingressClassName=webapprouting.kubernetes.azure.com"
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

    # Health checks (GCP-specific annotation check — skip for AWS ALB and Azure)
    if [[ "$CLOUD" == "gcp" ]]; then
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

do_step_11_deploy() {
  step_header 11 "$TOTAL_STEPS" "Terraform & Deploy"

  cd "$TF_DIR"
  dim "Working directory: ${TF_DIR}"
  echo ""

  # ─── Terraform Init ────────────────────────────────────────────────
  spinner_start "Running terraform init..."
  if [[ "$CLOUD" == "gcp" ]]; then
    TF_INIT_ARGS=(-input=false -backend-config="bucket=${TF_STATE_BUCKET}" -backend-config="prefix=${TF_STATE_PREFIX}")
  elif [[ "$CLOUD" == "aws" ]]; then
    TF_INIT_ARGS=(-input=false -backend-config="bucket=${TF_STATE_BUCKET}" -backend-config="key=${TF_STATE_KEY}" -backend-config="region=${REGION}" -backend-config="use_lockfile=true")
  elif [[ "$CLOUD" == "azure" ]]; then
    TF_INIT_ARGS=(-input=false -backend-config="resource_group_name=${TF_STATE_RG}" -backend-config="storage_account_name=${TF_STATE_SA}" -backend-config="container_name=${TF_STATE_CONTAINER}" -backend-config="key=${TF_STATE_KEY}")
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
  elif [[ "$CLOUD" == "azure" ]]; then
    ok "Terraform initialized ${DIM}(state: ${TF_STATE_SA}/${TF_STATE_CONTAINER}/${TF_STATE_KEY})${NC}"
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
  elif [[ "$CLOUD" == "azure" ]]; then
    spinner_start "Fetching cluster credentials..."
    AZURE_RG=$(terraform -chdir="$TF_DIR" output -raw resource_group_name 2>/dev/null || echo "${CLUSTER_NAME}-rg")
    AZ_KC_FILE=$(azure_kubeconfig_file)
    az aks get-credentials \
      --name "$CLUSTER_NAME" \
      --resource-group "$AZURE_RG" \
      --file "$AZ_KC_FILE" \
      --overwrite-existing > /dev/null 2>&1
    ensure_default_kubeconfig
    kubectl config use-context "$CLUSTER_NAME" > /dev/null 2>&1 || true
    spinner_stop
    ok "kubectl configured for ${CLUSTER_NAME}"

    # Update Helm values with actual client IDs from terraform outputs
    API_CLIENT_ID=$(terraform -chdir="$TF_DIR" output -raw api_identity_client_id 2>/dev/null || echo "")
    AGENT_CLIENT_ID=$(terraform -chdir="$TF_DIR" output -raw agent_identity_client_id 2>/dev/null || echo "")
    if [[ -n "$API_CLIENT_ID" && -f "$HELM_VALUES" ]]; then
      sed -i.bak "s|\${API_CLIENT_ID}|${API_CLIENT_ID}|g" "$HELM_VALUES" 2>/dev/null || \
        sed -i '' "s|\${API_CLIENT_ID}|${API_CLIENT_ID}|g" "$HELM_VALUES"
      sed -i.bak "s|\${AGENT_CLIENT_ID}|${AGENT_CLIENT_ID}|g" "$HELM_VALUES" 2>/dev/null || \
        sed -i '' "s|\${AGENT_CLIENT_ID}|${AGENT_CLIENT_ID}|g" "$HELM_VALUES"
      rm -f "${HELM_VALUES}.bak"
      ok "Updated Helm values with managed identity client IDs"

      # Add Key Vault URL if enabled
      if [[ "$ENABLE_KEY_VAULT" == "true" ]]; then
        KEY_VAULT_URI=$(terraform -chdir="$TF_DIR" output -raw key_vault_uri 2>/dev/null || echo "")
        if [[ -n "$KEY_VAULT_URI" ]]; then
          sed -i.bak "/SECRET_PROVIDER:/a\\
  SECRET_AZURE_VAULT_URL: \"${KEY_VAULT_URI}\"" "$HELM_VALUES" 2>/dev/null || \
            sed -i '' "/SECRET_PROVIDER:/a\\
  SECRET_AZURE_VAULT_URL: \"${KEY_VAULT_URI}\"" "$HELM_VALUES"
          rm -f "${HELM_VALUES}.bak"
          ok "Added Key Vault URI to Helm values"
        fi
      fi
    fi
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

  # Set TOTAL_STEPS before calling any step functions
  TOTAL_STEPS=$(( 11 + ${#PLUGIN_STEPS[@]} ))

  warn "Destroy mode: this will tear down ALL DecisionBox infrastructure."
  echo ""

  resolve_deployment_dir "destroy"

  parse_tfvar() { grep -E "^${1}[[:space:]]*=" "$TFVARS_FILE" 2>/dev/null | head -1 | sed 's/.*=[[:space:]]*//; s/"//g; s/[[:space:]]*$//' || true ; }

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
    ALLOWED_IP_RANGES=$(parse_tfvar_list allowed_ip_ranges)
    display_ip_restriction
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
    ALLOWED_IP_RANGES=$(parse_tfvar_list allowed_ip_ranges)
    display_ip_restriction
  elif [[ "$CLOUD" == "azure" ]]; then
    SUBSCRIPTION_ID=$(parse_tfvar subscription_id)
    LOCATION=$(parse_tfvar location)
    AZURE_RG=$(parse_tfvar resource_group_name)
    [[ -z "$AZURE_RG" ]] && AZURE_RG="${CLUSTER_NAME}-rg"
    ENABLE_KEY_VAULT=$(parse_tfvar enable_key_vault)
    ALLOWED_IP_RANGES=$(parse_tfvar_list allowed_ip_ranges)
    if [[ -z "$CLUSTER_NAME" ]]; then
      err "Failed to parse config from ${TFVARS_FILE}"
      exit 1
    fi
    echo -e "  ${BOLD}Provider:${NC}    Azure"
    echo -e "  ${BOLD}Subscription:${NC} ${SUBSCRIPTION_ID}"
    echo -e "  ${BOLD}Cluster:${NC}     ${CLUSTER_NAME}"
    echo -e "  ${BOLD}Resource group:${NC} ${AZURE_RG}"
    echo -e "  ${BOLD}Location:${NC}    ${LOCATION}"
    echo -e "  ${BOLD}Namespace:${NC}   ${K8S_NS}"
    echo -e "  ${BOLD}Key Vault:${NC}   ${ENABLE_KEY_VAULT}"
    display_ip_restriction
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
  elif [[ "$CLOUD" == "azure" ]]; then
    AZURE_RG=$(parse_tfvar resource_group_name)
    [[ -z "$AZURE_RG" ]] && AZURE_RG="${CLUSTER_NAME}-rg"
    AZ_KC_FILE=$(azure_kubeconfig_file)
    az aks get-credentials --name "$CLUSTER_NAME" --resource-group "$AZURE_RG" --file "$AZ_KC_FILE" --overwrite-existing > /dev/null 2>&1 && { ensure_default_kubeconfig; kubectl config use-context "$CLUSTER_NAME" > /dev/null 2>&1 || true; CLUSTER_REACHABLE=true; }
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
  elif [[ "$CLOUD" == "azure" ]]; then
    state_rg="" ; state_sa="" ; state_container="" ; state_key=""
    state_rg=$(grep 'resource_group_name' .terraform/terraform.tfstate 2>/dev/null | head -1 | sed 's/.*"resource_group_name":\s*"//; s/".*//' || echo "terraform-state-rg")
    state_sa=$(grep 'storage_account_name' .terraform/terraform.tfstate 2>/dev/null | head -1 | sed 's/.*"storage_account_name":\s*"//; s/".*//' || echo "")
    state_container=$(grep 'container_name' .terraform/terraform.tfstate 2>/dev/null | head -1 | sed 's/.*"container_name":\s*"//; s/".*//' || echo "tfstate")
    state_key=$(grep '"key"' .terraform/terraform.tfstate 2>/dev/null | head -1 | sed 's/.*"key":\s*"//; s/".*//' || echo "prod.terraform.tfstate")
    terraform init -input=false \
      -backend-config="resource_group_name=${state_rg}" \
      -backend-config="storage_account_name=${state_sa}" \
      -backend-config="container_name=${state_container}" \
      -backend-config="key=${state_key}" > /dev/null 2>&1 || {
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
  elif [[ "$CLOUD" == "azure" ]]; then
    echo -e "  ${BOLD}Subscription:${NC} ${SUBSCRIPTION_ID}"
  fi
  echo ""
  echo -e "  ${DIM}Destroy time: ${TF_DESTROY_SECS}s${NC}"
  if [[ "$CLOUD" == "gcp" ]]; then
    echo -e "  ${DIM}State bucket gs://${TF_STATE_BUCKET} still exists (contains state history)${NC}"
  elif [[ "$CLOUD" == "aws" ]]; then
    echo -e "  ${DIM}State bucket s3://${TF_STATE_BUCKET} still exists (contains state history)${NC}"
  elif [[ "$CLOUD" == "azure" ]]; then
    echo -e "  ${DIM}State storage account ${state_sa:-} still exists (contains state history)${NC}"
  fi
  echo ""
  exit 0
fi

# ─── Resume Mode ──────────────────────────────────────────────────────────

if [[ "$RESUME" == "true" ]]; then
  info "Resume mode: loading config from previous run..."
  echo ""

  resolve_deployment_dir "resume"

  # Parse tfvars (HCL key = "value" format)
  parse_tfvar() { grep -E "^${1}[[:space:]]*=" "$TFVARS_FILE" 2>/dev/null | head -1 | sed 's/.*=[[:space:]]*//; s/"//g; s/[[:space:]]*$//' || true ; }

  REGION=$(parse_tfvar region)
  CLUSTER_NAME=$(parse_tfvar cluster_name)
  K8S_NS=$(parse_tfvar k8s_namespace)
  HELM_DIR="${SCRIPT_DIR}/../helm-charts/decisionbox-api"
  HELM_VALUES="${TF_DIR}/values-secrets.yaml"

  if [[ "$CLOUD" == "gcp" ]]; then
    PROJECT_ID=$(parse_tfvar project_id)
    MACHINE_TYPE=$(parse_tfvar machine_type)
    MIN_NODES=$(parse_tfvar min_node_count)
    MAX_NODES=$(parse_tfvar max_node_count)
    ENABLE_SECRETS=$(parse_tfvar enable_gcp_secrets)
    SECRET_NS=$(parse_tfvar secret_namespace)
    BQ_IAM=$(parse_tfvar enable_bigquery_iam)
    VERTEX_AI_IAM=$(parse_tfvar enable_vertex_ai_iam)
    ALLOWED_IP_RANGES=$(parse_tfvar_list allowed_ip_ranges)
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
    ALLOWED_IP_RANGES=$(parse_tfvar_list allowed_ip_ranges)
    if [[ -z "$CLUSTER_NAME" || -z "$K8S_NS" ]]; then
      err "Failed to parse required values from ${TFVARS_FILE}"
      exit 1
    fi
  elif [[ "$CLOUD" == "azure" ]]; then
    SUBSCRIPTION_ID=$(parse_tfvar subscription_id)
    LOCATION=$(parse_tfvar location)
    REGION="$LOCATION"
    AZURE_RG=$(parse_tfvar resource_group_name)
    [[ -z "$AZURE_RG" ]] && AZURE_RG="${CLUSTER_NAME}-rg"
    VM_SIZE=$(parse_tfvar vm_size)
    MIN_NODES=$(parse_tfvar min_node_count)
    MAX_NODES=$(parse_tfvar max_node_count)
    ENABLE_KEY_VAULT=$(parse_tfvar enable_key_vault)
    ENABLE_SECRETS="$ENABLE_KEY_VAULT"
    SECRET_NS=$(parse_tfvar secret_namespace)
    [[ -z "$SECRET_NS" ]] && SECRET_NS="decisionbox"
    ALLOWED_IP_RANGES=$(parse_tfvar_list allowed_ip_ranges)
    HELM_DIR="${SCRIPT_DIR}/../helm-charts/decisionbox-api"
    HELM_VALUES="${HELM_DIR}/values-secrets.yaml"
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
  display_ip_restriction
  echo ""

  # Set TOTAL_STEPS before calling any step functions
  TOTAL_STEPS=$(( 11 + ${#PLUGIN_STEPS[@]} ))

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
  elif [[ "$CLOUD" == "azure" ]]; then
    AZURE_RG=$(parse_tfvar resource_group_name)
    [[ -z "$AZURE_RG" ]] && AZURE_RG="${CLUSTER_NAME}-rg"
    AZ_KC_FILE=$(azure_kubeconfig_file)
    az aks get-credentials \
      --name "$CLUSTER_NAME" \
      --resource-group "$AZURE_RG" \
      --file "$AZ_KC_FILE" \
      --overwrite-existing > /dev/null 2>&1 || true
    ensure_default_kubeconfig
    kubectl config use-context "$CLUSTER_NAME" > /dev/null 2>&1 || true
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
    elif [[ "$CLOUD" == "azure" ]]; then
      dim "Check: az aks list --resource-group ${AZURE_RG:-${CLUSTER_NAME}-rg}"
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
      elif [[ "$CLOUD" == "azure" ]]; then
        DASH_ARGS+=(
          --set "ingress.ingressClassName=webapprouting.kubernetes.azure.com"
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

# ─── Source plugin files ──────────────────────────────────────────────────
# Plugins register extra steps via register_step(). These run between
# "Terraform State" and "Review" in the wizard flow.

for include_file in ${INCLUDE_FILES[@]+"${INCLUDE_FILES[@]}"}; do
  if [[ ! -f "$include_file" ]]; then
    err "Include file not found: ${include_file}"
    exit 1
  fi
  source "$include_file"
done

# Recalculate total steps after plugins registered
TOTAL_STEPS=$(( 11 + ${#PLUGIN_STEPS[@]} ))

# ─── Normal Flow ──────────────────────────────────────────────────────────

dim "Type 'back' at any prompt to return to the previous step."

do_step_1_prerequisites

# Build navigable step list: core steps + plugin steps + review
NAV_STEPS=(
  do_step_2_deployment
  do_step_3_cloud_provider
  do_step_4_secrets
  do_step_5_provider_config
  do_step_6_vector_search
  do_step_7_authentication
  do_step_8_terraform_state
)

# Insert plugin steps before review
for i in ${!PLUGIN_STEPS[@]+"${!PLUGIN_STEPS[@]}"}; do
  NAV_STEPS+=("${PLUGIN_STEPS[$i]}")
done

NAV_STEPS+=(do_step_9_review)

CURRENT_STEP=0

while [[ "$CURRENT_STEP" -lt ${#NAV_STEPS[@]} ]]; do
  "${NAV_STEPS[$CURRENT_STEP]}" || true

  if [[ "$GO_BACK" == "true" ]]; then
    GO_BACK=false
    if [[ "$CURRENT_STEP" -gt 0 ]]; then
      CURRENT_STEP=$((CURRENT_STEP - 1))
    else
      info "Already at the first configurable step."
    fi
  else
    CURRENT_STEP=$((CURRENT_STEP + 1))
  fi
done

do_step_10_generate
do_step_11_deploy
