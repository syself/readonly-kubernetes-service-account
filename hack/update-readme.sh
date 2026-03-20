#!/usr/bin/env bash
# Bash Strict Mode: https://github.com/guettli/bash-strict-mode
trap 'echo -e "\nWarning: command failed at ($0:$LINENO): $(sed -n "${LINENO}p" "$0" 2>/dev/null || true)" >&2; exit 3' ERR
set -Eeuo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

binary="$tmpdir/readonly-kubernetes-service-account"
usage_file="$tmpdir/usage.txt"

cd "$repo_root"
go build -o "$binary" .

if "$binary" >/dev/null 2>"$usage_file"; then
	status=0
else
	status=$?
fi

if [[ $status -ne 2 ]]; then
	echo "expected usage exit code 2, got $status" >&2
	exit 1
fi

usage_text="$(cat "$usage_file")"

cat >"$repo_root/README.md" <<EOF
# readonly-kubernetes-service-account

Generate YAML for a readonly Kubernetes service account.

## Usage

\`\`\`text
$usage_text
\`\`\`

## Example

\`\`\`bash
go run . example-sa > readonly-service-account.yaml
kubectl apply -f readonly-service-account.yaml
\`\`\`

## Development

Regenerate this README with:

\`\`\`bash
./hack/update-readme.sh
\`\`\`
EOF
