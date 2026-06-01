#!/usr/bin/env bash
# Emits build metadata (version, commit, build_date) as `key=value` lines.
# Used two ways, both stamping the same values into the images:
#   - CI: appended to $GITHUB_OUTPUT, then passed to the image builds as
#     --build-arg VERSION/COMMIT/BUILD_DATE.
#   - Local `make docker-build`: eval'd into shell vars for --build-arg.
# The Dockerfiles inject them into the binaries (-ldflags) and the
# dashboard bundle; GET /api/v1/system surfaces the result.
#
# Pre-set VERSION/COMMIT/BUILD_DATE env vars are honored as-is and only
# the missing pieces are computed. `make docker-build` uses this to compute
# one stamp and reuse it across the API/Agent/Dashboard images so they
# report identical metadata.
#
# Version source, when not pre-set:
#   - tag build  → the tag without its leading "v" (v0.10.0 → 0.10.0)
#   - CI non-tag → 0.0.0-dev+<short-sha>
#   - local      → `git describe` (or "dev" outside a git checkout)
set -euo pipefail

commit="${COMMIT:-}"
version="${VERSION:-}"
build_date="${BUILD_DATE:-}"

if [ -z "$commit" ]; then
  if [ -n "${GITHUB_SHA:-}" ]; then
    commit="${GITHUB_SHA:0:12}"
  elif commit="$(git rev-parse --short=12 HEAD 2>/dev/null)"; then
    :
  else
    commit="unknown"
  fi
fi

if [ -z "$version" ]; then
  if [ "${GITHUB_REF_TYPE:-}" = "tag" ] && [ -n "${GITHUB_REF_NAME:-}" ]; then
    version="${GITHUB_REF_NAME#v}"
  elif [ -n "${GITHUB_SHA:-}" ]; then
    version="0.0.0-dev+${commit}"
  elif version="$(git describe --tags --always --dirty 2>/dev/null)"; then
    version="${version#v}"
  else
    version="dev"
  fi
fi

if [ -z "$build_date" ]; then
  build_date="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
fi

echo "version=${version}"
echo "commit=${commit}"
echo "build_date=${build_date}"
