#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: scripts/build-metadata.sh [--shell|--ldflags|--summary]

Print CLIProxyAPI build metadata derived from the current Git checkout.

Outputs:
  --shell    VERSION=..., COMMIT=..., BUILD_DATE=... lines for eval
  --ldflags  go build -ldflags value with Version/Commit/BuildDate
  --summary  human-readable metadata summary (default)
EOF
}

mode=summary
case "${1:-}" in
  ""|--summary) mode=summary ;;
  --shell) mode=shell ;;
  --ldflags) mode=ldflags ;;
  -h|--help) usage; exit 0 ;;
  *)
    echo "unknown option: $1" >&2
    usage >&2
    exit 2
    ;;
esac

repo_root=$(git rev-parse --show-toplevel 2>/dev/null || pwd)
cd "$repo_root"

commit=$(git rev-parse --short=8 HEAD 2>/dev/null || echo none)
build_date=${CLI_PROXY_BUILD_DATE:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}

base_tag=$(git describe --tags --abbrev=0 --match 'v[0-9]*' HEAD 2>/dev/null || true)
if [[ -z "$base_tag" ]]; then
  version=$(git describe --tags --always --dirty 2>/dev/null || echo dev)
else
  version="$base_tag"
fi

ldflags="-s -w -X main.Version=${version} -X main.Commit=${commit} -X main.BuildDate=${build_date}"

case "$mode" in
  shell)
    printf 'VERSION=%q\n' "$version"
    printf 'COMMIT=%q\n' "$commit"
    printf 'BUILD_DATE=%q\n' "$build_date"
    ;;
  ldflags)
    printf '%s\n' "$ldflags"
    ;;
  summary)
    printf 'Version: %s\n' "$version"
    printf 'Commit: %s\n' "$commit"
    printf 'Build Date: %s\n' "$build_date"
    ;;
esac
