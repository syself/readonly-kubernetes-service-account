#!/usr/bin/env bash
# Bash Strict Mode: https://github.com/guettli/bash-strict-mode
trap 'echo -e "\nWarning: command failed at ($0:$LINENO): $(sed -n "${LINENO}p" "$0" 2>/dev/null || true)" >&2; exit 3' ERR
set -Eeuo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

binary="$tmpdir/readonly-kubernetes-service-account"
readonly readme_file="$repo_root/README.md"

cd "$repo_root"
go build -o "$binary" .

# usage_text prints the help of the command. The binary exits with 2 after printing help.
usage_text() {
	local usage_file="$tmpdir/usage.txt"
	local status=0
	"$binary" "$@" --help >/dev/null 2>"$usage_file" || status=$?
	if [[ $status -ne 2 ]]; then
		echo "expected usage exit code 2 for '$*', got $status" >&2
		exit 1
	fi
	cat "$usage_file"
}

# replace_block replaces the text between the markers with the given usage text.
replace_block() {
	local marker="$1"
	local text="$2"
	local start_marker="<!-- $marker:start -->"
	local end_marker="<!-- $marker:end -->"
	local block
	block="$(
		cat <<EOF
$start_marker
\`\`\`text
$text
\`\`\`
$end_marker
EOF
	)"

	if ! grep -Fq "$start_marker" "$readme_file" || ! grep -Fq "$end_marker" "$readme_file"; then
		echo "README markers not found: $start_marker ... $end_marker" >&2
		exit 1
	fi

	START_MARKER="$start_marker" \
	END_MARKER="$end_marker" \
	UPDATED_BLOCK="$block" \
	perl -0pi -e '
		my $start = quotemeta($ENV{START_MARKER});
		my $end = quotemeta($ENV{END_MARKER});
		my $replacement = $ENV{UPDATED_BLOCK};
		my $count = s/$start.*?$end/$replacement/s;
		die "failed to update block $ENV{START_MARKER}\n" if $count != 1;
	' "$readme_file"
}

replace_block usage "$(usage_text)"
replace_block kubeconfig-usage "$(usage_text kubeconfig)"
