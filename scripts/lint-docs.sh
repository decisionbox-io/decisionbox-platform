#!/usr/bin/env bash
# Lint public-documentation surfaces for hygiene violations.
#
# Forbidden tokens (any hit exits non-zero):
#
#   Plan / version / review-round leakage (Principles 1, 2, 3)
#     - Plan filenames     : PLAN-[A-Z][A-Z0-9-]*\.md
#     - Plan paths         : (open-source/)?plans/
#     - Phase labels       : Phase [A-Z]\b      (architecture IDs "Phase 4.5" / "Phase 5.5" are digits → not matched)
#     - MVP markers        : \bMVP\b
#     - Review-round       : Codex (prod-)?r[0-9]
#     - Plan-version       : plan v[0-9], pre-plan(-v[0-9])?
#     - Hand-rolled vX.Y.Z : \bv[0-9]+\.[0-9]+\.[0-9]+\b (in body prose only)
#
#   Enterprise leakage (Principle 7)
#     - Word "enterprise"  : [Ee]nterprise
#     - Repo paths         : decisionbox-enterprise, decisionbox-cloud-enterprise-tenant
#     - Image names        : -enterprise:[0-9.]+, decisionbox-(api|dashboard)-(cloud-)?enterprise
#     - Enterprise env vars: AUTH_ENABLED, OIDC_*, AUDIT_LOG_*, GOVERNANCE_*, SLACK_NOTIFICATION_*, BREVO_*
#
# Acronyms RBAC / OIDC / SAML / SSO are NOT regex-matched: they collide with
# legitimate cloud-provider RBAC / OIDC (Azure / AWS / Kubernetes) too often.
# Paid-feature framing in body prose is caught by the Codex per-page review
# (Phase 3 of PLAN-DOCS-HYGIENE) which can reason about context.
#
# Per-line override: append "<!-- lint-allow: <reason> -->" to the offending line.
#
# Usage:
#   scripts/lint-docs.sh                       # scans ./docs (this repo)
#   scripts/lint-docs.sh docs more/docs        # scans listed dirs
#   scripts/lint-docs.sh -v                    # verbose: print every match
#
# Exit codes: 0 = clean, 1 = violations, 2 = bad invocation.

set -euo pipefail

verbose=0
roots=()
for arg in "$@"; do
  case "$arg" in
    -v|--verbose) verbose=1 ;;
    *) roots+=("$arg") ;;
  esac
done
if [[ ${#roots[@]} -eq 0 ]]; then
  roots=("docs")
fi

# Each line: name|grep -P regex
PATTERNS=$(cat <<'EOF'
plan-filename|PLAN-[A-Z][A-Z0-9-]*\.md
plan-path|(?:open-source/)?plans/
phase-label|Phase [A-Z](?![a-z0-9])
mvp-marker|\bMVP\b
review-round|Codex (?:prod-)?r[0-9]
plan-version|plan v[0-9]
plan-version|pre-plan(?:-v[0-9])?
version-marker|\bv[0-9]+\.[0-9]+\.[0-9]+\b
enterprise-word|[Ee]nterprise
enterprise-repo|decisionbox-enterprise
enterprise-repo|decisionbox-cloud-enterprise-tenant
enterprise-image|-enterprise:[0-9.]+
enterprise-image|decisionbox-(?:api|dashboard)-(?:cloud-)?enterprise
enterprise-env|\bAUTH_ENABLED\b
enterprise-env|\bOIDC_[A-Z_]+\b
enterprise-env|\bAUDIT_LOG_[A-Z_]+\b
enterprise-env|\bGOVERNANCE_[A-Z_]+\b
enterprise-env|\bSLACK_NOTIFICATION_[A-Z_]+\b
enterprise-env|\bBREVO_[A-Z_]+\b
EOF
)

# Strip fenced code blocks (```...```), ::: callouts, and markdown link
# reference lines (`[anchor]: url`). Preserve original line numbers by
# replacing stripped lines with blanks.
strip_blocks() {
  awk '
    BEGIN { fenced=0; callout=0 }
    /^```/                 { fenced  = !fenced;  print ""; next }
    /^:::/                 { callout = !callout; print ""; next }
    /^\[[^]]+\]:[[:space:]]+http/ {                         print ""; next }
    {
      if (fenced || callout) print ""
      else                   print $0
    }
  ' "$1"
}

# Scan one file. Echos one line per violation:
#   file:line: [pattern-name] content
scan_file() {
  local file="$1"
  local stripped
  stripped=$(strip_blocks "$file")
  while IFS='|' read -r name pattern; do
    [[ -z "$name" ]] && continue
    # grep -P for PCRE (lookahead in phase-label).
    while IFS= read -r hit; do
      [[ -z "$hit" ]] && continue
      local lineno content
      lineno=${hit%%:*}
      content=${hit#*:}
      # Per-line whitelist
      if [[ "$content" == *"<!-- lint-allow:"* ]]; then
        continue
      fi
      echo "${file}:${lineno}: [${name}] ${content}"
    done < <(printf '%s\n' "$stripped" | grep -nP "$pattern" || true)
  done <<< "$PATTERNS"
}

# -------- main
violations=0
files_scanned=0
for root in "${roots[@]}"; do
  if [[ ! -d "$root" ]]; then
    echo "lint-docs: not a directory: $root" >&2
    exit 2
  fi
  while IFS= read -r -d '' file; do
    files_scanned=$((files_scanned + 1))
    hits=$(scan_file "$file")
    if [[ -n "$hits" ]]; then
      while IFS= read -r line; do
        violations=$((violations + 1))
        if [[ $verbose -eq 1 ]]; then
          echo "$line"
        fi
      done <<< "$hits"
      if [[ $verbose -eq 0 ]]; then
        echo "$hits"
      fi
    fi
  done < <(find "$root" -type f \( -name '*.md' -o -name '*.mdx' \) -print0)
done

if [[ $violations -gt 0 ]]; then
  echo ""
  echo "lint-docs: $violations violation(s) across $files_scanned file(s)." >&2
  exit 1
fi

echo "lint-docs: clean ($files_scanned files)."
exit 0
