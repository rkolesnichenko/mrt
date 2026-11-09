#!/bin/sh
# Runs every fuzz target for FUZZTIME each (default 15s). check.sh runs it when
# FUZZTIME is set; CI runs it as its own job, split into shards:
#
#   scripts/fuzz.sh                      # every target, 15s each
#   FUZZTIME=1m scripts/fuzz.sh          # every target, 1m each
#   FUZZ_SHARD=2/3 scripts/fuzz.sh       # the 2nd of 3 shards (every 3rd target)
#
# FUZZMINIMIZETIME (default 2s) bounds how long the fuzzer minimizes each new
# input. Go's default is 60s, during which no new inputs are tried, so a run of
# 15s to 30s could be spent entirely minimizing. A failing input is written
# whole even when its minimization is cut short.
#
# FUZZ_BASELINE_MAX (seconds, unset by default) fails a target whose baseline
# coverage over the cached corpus took longer. The daily run in fuzz.yml sets
# it, well under the 30s a push or pull request gives each target, so that a
# corpus grown too large for those runs fails the daily run (which then saves
# no cache) and not an unrelated pull request.
#
# Every target needs a seed (f.Add or testdata/fuzz): with neither and an
# empty cache, Go fuzzes without the baseline step, never prints "now
# fuzzing", and the check below fails it.
set -u
cd "$(dirname "$0")/.."
FUZZTIME=${FUZZTIME:-15s}
FUZZMINIMIZETIME=${FUZZMINIMIZETIME:-2s}
FUZZ_SHARD=${FUZZ_SHARD:-1/1}
FUZZ_BASELINE_MAX=${FUZZ_BASELINE_MAX:-}
case "$FUZZ_BASELINE_MAX" in
*[!0-9]*) echo "fuzz: FUZZ_BASELINE_MAX must be a number of seconds, not $FUZZ_BASELINE_MAX"; exit 2 ;;
esac
shard=${FUZZ_SHARD%%/*}
shards=${FUZZ_SHARD#*/}
# Digits, one slash, digits: "2" alone is not shard 2 of 2. No leading zero,
# which $(( )) would read as octal.
case "$FUZZ_SHARD" in
*[!0-9/]* | */*/* | /* | */ | 0* | */0*) echo "fuzz: FUZZ_SHARD must be i/n, not $FUZZ_SHARD"; exit 2 ;;
*/*) ;;
*) echo "fuzz: FUZZ_SHARD must be i/n, not $FUZZ_SHARD"; exit 2 ;;
esac
if [ "$shards" -lt 1 ] || [ "$shard" -lt 1 ] || [ "$shard" -gt "$shards" ]; then
	echo "fuzz: FUZZ_SHARD must be i/n with 1 <= i <= n, not $FUZZ_SHARD"
	exit 2
fi
fail=0
step() { printf '\n== %s\n' "$*"; }
bad() { echo "FAIL: $*"; fail=1; }

# fuzz runs one fuzz target. The Go fuzzing harness sometimes ends a run that
# found nothing with "context deadline exceeded" when a worker is slow to stop
# at -fuzztime; a real crash always prints "Failing input written to". So a run
# that fails that way without a failing input is retried once.
#
# Go starts the -fuzztime clock before the run over the cached corpus that
# gathers baseline coverage, and a run whose corpus takes all of FUZZTIME ends
# there and passes, having fuzzed nothing. Such a run fails here instead: Go
# prints "now fuzzing" once the baseline is complete.
fuzz() {
	out=$(cd "$1" && go test -run='^$' -fuzz="^$3\$" -fuzztime="$FUZZTIME" -fuzzminimizetime="$FUZZMINIMIZETIME" "$2" 2>&1)
	rc=$?
	printf '%s\n' "$out"
	if [ "$rc" -ne 0 ] && printf '%s' "$out" | grep -q "context deadline exceeded" &&
		! printf '%s' "$out" | grep -q "Failing input written"; then
		step "fuzz $3: harness deadline without a failing input; retrying once"
		out=$(cd "$1" && go test -run='^$' -fuzz="^$3\$" -fuzztime="$FUZZTIME" -fuzzminimizetime="$FUZZMINIMIZETIME" "$2" 2>&1)
		rc=$?
		printf '%s\n' "$out"
	fi
	[ "$rc" -eq 0 ] || return "$rc"
	if printf '%s' "$out" | grep -q "no fuzz tests to fuzz"; then
		echo "fuzz $3: no such fuzz target in $1 (renamed? update the list below)"
		return 1
	fi
	if ! printf '%s' "$out" | grep -q "now fuzzing"; then
		echo "fuzz $3: FUZZTIME $FUZZTIME ended before the baseline coverage of the cached corpus did; nothing was fuzzed"
		corpus_help "$3" "or raise FUZZTIME (and FUZZ_BASELINE_MAX in fuzz.yml with it)."
		return 1
	fi
	# Seconds the baseline took: Go prints "fuzz: elapsed: 1m3s, gathering
	# baseline coverage: N/N completed, now fuzzing with W workers".
	took=$(printf '%s\n' "$out" | sed -n 's/^fuzz: elapsed: \([0-9hms]*\), .*now fuzzing.*/\1/p' | head -n 1 |
		awk '{ t = 0; n = $0
			if (match(n, /[0-9]+h/)) t += substr(n, RSTART, RLENGTH - 1) * 3600
			if (match(n, /[0-9]+m/)) t += substr(n, RSTART, RLENGTH - 1) * 60
			if (match(n, /[0-9]+s/)) t += substr(n, RSTART, RLENGTH - 1)
			print t }')
	[ -n "$FUZZ_BASELINE_MAX" ] || return 0
	if [ -z "$took" ]; then
		echo "fuzz $3: could not read how long the baseline coverage took (has Go's output changed?)"
		return 1
	fi
	if [ "$took" -gt "$FUZZ_BASELINE_MAX" ]; then
		echo "fuzz $3: baseline coverage of the cached corpus took ${took}s, over FUZZ_BASELINE_MAX ${FUZZ_BASELINE_MAX}s"
		corpus_help "$3" "or raise FUZZ_BASELINE_MAX in fuzz.yml with ci.yml's fuzztime."
		return 1
	fi
	return 0
}

# corpus_help says how to start target $1's cached corpus afresh; $2 is the
# other way out.
corpus_help() {
	echo "  The corpus Go keeps in \$(go env GOCACHE)/fuzz has grown too large."
	echo "  Locally: remove \$(go env GOCACHE)/fuzz/<package path>/$1."
	echo "  In CI: delete the corpus caches (gh cache list --key fuzz-corpus-, then gh cache delete"
	echo "  each key; fuzz.yml removes setup-go's copy of the corpus, so these are the only source),"
	echo "  $2"
}

# One target per line: "<dir> <pkg> <FuzzName>".
targets='
. . FuzzRecords
. . FuzzRoutes
bgp . FuzzParseUpdate
bgp . FuzzDecodeAttrs
bgp . FuzzASPathMerge
bgp . FuzzParseOpen
bmp . FuzzMessages
bmp . FuzzBMPRoutes
bmp . FuzzBMPMatchesMRT
bmp/openbmp . FuzzOpenBMP
internal/bzip2 . FuzzBzip2
. . FuzzDecompress
'

# dir package fuzz-target; shard i of n takes targets i, i+n, i+2n, ...
i=0
ran=0
oldifs=$IFS
IFS='
'
for t in $targets; do
	IFS=$oldifs
	i=$((i + 1))
	[ $(((i - 1) % shards + 1)) -eq "$shard" ] || continue
	ran=$((ran + 1))
	set -- $t
	step "fuzz $3 ($FUZZTIME)"
	fuzz "$1" "$2" "$3" || bad "fuzz $3"
done
# A shard past the last target would otherwise pass having fuzzed nothing.
if [ "$ran" -eq 0 ]; then
	echo "fuzz: shard $FUZZ_SHARD runs no target (there are $i)"
	exit 2
fi

echo
if [ "$fail" -ne 0 ]; then echo "fuzz: FAILED"; exit 1; fi
echo "fuzz: ok"
