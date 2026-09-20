#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: scripts/build-server.sh [-o OUTPUT]

Build the CLIProxyAPI server with version metadata for the current Git checkout.
Honors GOOS, GOARCH, CGO_ENABLED, and other standard Go environment variables.
EOF
}

output=./CLIProxyAPI
while [[ $# -gt 0 ]]; do
  case "$1" in
    -o|--output)
      if [[ $# -lt 2 ]]; then
        echo "missing value for $1" >&2
        exit 2
      fi
      output=$2
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "unknown option: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

repo_root=$(git rev-parse --show-toplevel 2>/dev/null || pwd)
cd "$repo_root"

metadata=$(scripts/build-metadata.sh --shell)
eval "$metadata"
ldflags=$(CLI_PROXY_BUILD_DATE="$BUILD_DATE" scripts/build-metadata.sh --ldflags)

printf 'Building CLIProxyAPI server\n'
printf '  Version: %s\n' "$VERSION"
printf '  Commit: %s\n' "$COMMIT"
printf '  Build Date: %s\n' "$BUILD_DATE"
printf '  Output: %s\n' "$output"

go build -buildvcs=false -ldflags "$ldflags" -o "$output" ./cmd/server
