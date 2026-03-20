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

readonly readme_file="$repo_root/README.md"
readonly usage_start_marker="<!-- usage:start -->"
readonly usage_end_marker="<!-- usage:end -->"
readonly updated_usage_block="$(cat <<EOF
$usage_start_marker
\`\`\`text
$usage_text
\`\`\`
$usage_end_marker
EOF
)"

if ! grep -Fq "$usage_start_marker" "$readme_file" || ! grep -Fq "$usage_end_marker" "$readme_file"; then
	echo "README markers not found: $usage_start_marker ... $usage_end_marker" >&2
	exit 1
fi

START_MARKER="$usage_start_marker" \
END_MARKER="$usage_end_marker" \
UPDATED_USAGE_BLOCK="$updated_usage_block" \
perl -0pi -e '
	my $start = quotemeta($ENV{START_MARKER});
	my $end = quotemeta($ENV{END_MARKER});
	my $replacement = $ENV{UPDATED_USAGE_BLOCK};
	my $count = s/$start.*?$end/$replacement/s;
	die "failed to update Usage section\n" if $count != 1;
' "$readme_file"
