#!/bin/sh
# Measures what `codeforge gate` costs on a branch: tier, model calls, tokens, cost and wall time per run.
#
#   scripts/gate-cost.sh [-n RUNS] [-b CODEFORGE_BIN] [-r REPO_DIR] [-f]
#
# Each run starts from an empty verdict cache and no saved follow-up round, so it pays full price. That clears
# ~/.codeforge/review-cache and ~/.codeforge/review-rounds, which only hold re-creatable review state. Without -f
# the script asks first. Results are appended to $OUT (default: a temp file) as one JSON line per gate run.
#
# To compare versions, run it once per binary on the same branch and compare the printed totals. The usage
# fields exist only in builds that include gateusage.go; an older binary still reports wall time and verdict.
set -eu

runs=1
bin=codeforge
repo=.
force=0
while getopts "n:b:r:f" opt; do
	case $opt in
	n) runs=$OPTARG ;;
	b) bin=$OPTARG ;;
	r) repo=$OPTARG ;;
	f) force=1 ;;
	*) exit 2 ;;
	esac
done

out=${OUT:-$(mktemp -t gate-cost.XXXXXX)}
state=${HOME}/.codeforge

if [ "$force" != 1 ]; then
	printf 'This clears %s/review-cache and %s/review-rounds. Continue? [y/N] ' "$state" "$state"
	read -r answer
	[ "$answer" = y ] || exit 1
fi

cd "$repo"
i=1
while [ "$i" -le "$runs" ]; do
	rm -rf "$state/review-cache" "$state/review-rounds"
	echo "== run $i of $runs"
	start=$(date +%s)
	CODEFORGE_GATE_USAGE=$out "$bin" gate || echo "(gate exited $?; a blocking finding is a normal result)"
	echo "wall: $(($(date +%s) - start))s"
	i=$((i + 1))
done

echo
echo "usage lines: $out"
if [ ! -s "$out" ]; then
	echo "no usage lines were written: this binary does not report usage"
elif command -v jq >/dev/null 2>&1; then
	jq -r '[.tier, (.models|length), .totals.Calls, .totals.Steps, .totals.Input, .totals.CacheRead, .totals.Output, .totals.Cost, .elapsedSeconds, .passed] | @tsv' "$out" |
		awk 'BEGIN{print "tier\tmodels\tcalls\tsteps\tinput\tcached\toutput\tcost\tsecs\tpassed"} {print}'
else
	cat "$out"
fi
