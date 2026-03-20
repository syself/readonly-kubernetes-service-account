#!/usr/bin/env bash
# Bash Strict Mode: https://github.com/guettli/bash-strict-mode
trap 'echo -e "\nWarning: a command failed. Exiting. Line ($0:$LINENO): $(sed -n "${LINENO}p" "$0" 2>/dev/null || true)\n" >&2; exit 3' ERR
set -Eeuo pipefail

readonly script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly repo_root="$(cd -- "$script_dir/.." && pwd)"
readonly golangci_lint_version="v2.11.3"
tmpdir=""

if [[ -n ${GOBIN:-} ]]; then
	readonly gobin="$GOBIN"
else
	tmpdir="$(mktemp -d)"
	readonly gobin="$tmpdir/bin"
fi

cleanup() {
	if [[ -n "$tmpdir" ]]; then
		rm -rf "$tmpdir"
	fi
}

trap cleanup EXIT

mkdir -p "$gobin"
readonly golangci_lint_binary="$gobin/golangci-lint"

if [[ -x "$golangci_lint_binary" ]] && "$golangci_lint_binary" version | grep -Eq "(^| )${golangci_lint_version#v}([[:space:]]|$)|(^| )${golangci_lint_version}([[:space:]]|$)"; then
	echo "Using cached golangci-lint $golangci_lint_version"
else
	echo "Installing golangci-lint $golangci_lint_version"
	GOBIN="$gobin" go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@"$golangci_lint_version"
fi

echo "Running golangci-lint"
(
	cd "$repo_root"
	"$golangci_lint_binary" run ./...
)
