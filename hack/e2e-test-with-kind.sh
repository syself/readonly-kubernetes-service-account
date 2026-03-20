#!/usr/bin/env bash
# Bash Strict Mode: https://github.com/guettli/bash-strict-mode
trap 'echo -e "\nWarning: a command failed. Exiting. Line ($0:$LINENO): $(sed -n "${LINENO}p" "$0" 2>/dev/null || true)\n" >&2; exit 3' ERR
set -Eeuo pipefail

usage() {
	cat >&2 <<'EOF'
Usage: ./hack/e2e-test-with-kind.sh run

Creates a temporary kind cluster with a random name, runs `go run . e2e-readonly-sa`
against that cluster, and compares the generated YAML with the checked-in fixture at
`hack/e2e-test-with-kind/expected.yaml`.

The kind cluster and temporary kubeconfig are deleted automatically on exit.
EOF
}

if [[ $# -ne 1 || ${1:-} != "run" ]]; then
	usage
	exit 2
fi

readonly script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly repo_root="$(cd -- "$script_dir/.." && pwd)"
readonly expected_file="$script_dir/e2e-test-with-kind/expected.yaml"
readonly actual_file="$(mktemp)"
readonly kubeconfig_file="$(mktemp)"
readonly kind_image="kindest/node:v1.33.1"
readonly service_account_name="e2e-readonly-sa"
cluster_name=""

cleanup() {
	local exit_code=$?
	trap - EXIT INT TERM
	if [[ -n "$cluster_name" ]]; then
		kind delete cluster --name "$cluster_name" >/dev/null 2>&1 || true
	fi
	rm -f "$actual_file" "$kubeconfig_file"
	exit "$exit_code"
}

trap cleanup EXIT INT TERM

cluster_name="readonly-sa-e2e-$(LC_ALL=C od -An -N4 -tx1 /dev/urandom | tr -d ' \n')"

echo "Creating kind cluster $cluster_name using $kind_image"
kind create cluster \
	--name "$cluster_name" \
	--image "$kind_image" \
	--wait 120s \
	--kubeconfig "$kubeconfig_file"

echo "Generating YAML with go run . $service_account_name"
(
	cd "$repo_root"
	KUBECONFIG="$kubeconfig_file" go run . "$service_account_name" >"$actual_file"
)

echo "Comparing generated YAML with $expected_file"
diff -u "$expected_file" "$actual_file"

echo "kind e2e test passed"
