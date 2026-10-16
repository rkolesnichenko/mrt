#!/bin/sh
# Downloads the opt-in real-data set (spec §7.5) into .data/realdata
# (git-ignored; never committed — RIPE NCC's terms forbid redistributing RIS
# files, Ruling R18):
#
#   scripts/fetch-mrt.sh
#
# The newest of each, found by listing the archive's month directory:
#   - RIPE RIS rrc00 bview (gzip),
#   - route-views2 and route-views6 RIBs (bzip2),
#   - one hour of rrc00 updates (12 five-minute files) and route-views2 updates
#     (4 fifteen-minute files): the newest hour both archives hold in full.
#
# Each URL it chose is printed and pinned in .data/realdata/MANIFEST (one line
# per file: local name, URL, size in bytes, sha256), which TestRealData reads.
# A file already present is not downloaded again; a new run therefore pins the
# new newest files beside the old ones and rewrites MANIFEST to the new set.
set -eu
cd "$(dirname "$0")/.."

RIS=https://data.ris.ripe.net/rrc00
RV2=https://archive.routeviews.org/bgpdata
RV6=https://archive.routeviews.org/route-views6/bgpdata
out=.data/realdata
mkdir -p "$out"

if command -v shasum >/dev/null; then
	sha256() { shasum -a 256 "$1" | cut -d' ' -f1; }
elif command -v sha256sum >/dev/null; then
	sha256() { sha256sum "$1" | cut -d' ' -f1; }
else
	echo "fetch-mrt: neither shasum nor sha256sum is installed" >&2
	exit 1
fi

tmp=$(mktemp -d "${TMPDIR:-/tmp}/fetch-mrt.XXXXXX")
trap 'rm -rf "$tmp"' EXIT
trap 'exit 130' INT TERM

# This month and the one before (the first hours of a month may have nothing yet).
this=$(date -u +%Y.%m)
prev=$(echo "$this" | awk -F. '{ y = $1; m = $2 - 1; if (m == 0) { y--; m = 12 } printf "%04d.%02d\n", y, m }')

# names URL REGEXP: the file names in the directory listing at URL matching
# REGEXP, sorted; nothing when the directory does not exist.
names() {
	curl -fsS --retry 3 --max-time 120 "$1/" 2>/dev/null | grep -o "$2" | sort -u || true
}

# newest DIR-PATTERN REGEXP: "MONTH NAME" of the newest matching file in this
# month's directory, else last month's. DIR-PATTERN has @ where the month goes.
newest() {
	for month in "$this" "$prev"; do
		n=$(names "$(echo "$1" | sed "s|@|$month|")" "$2" | tail -1)
		if [ -n "$n" ]; then
			echo "$month $n"
			return
		fi
	done
	echo "fetch-mrt: nothing matching $2 under $1 for $this or $prev" >&2
	exit 1
}

# hours DIR-PATTERN REGEXP PER-HOUR: the hours (YYYYMMDD.HH) for which the
# directory lists PER-HOUR files, in both months.
hours() {
	for month in "$prev" "$this"; do
		names "$(echo "$1" | sed "s|@|$month|")" "$2"
	done | awk -F. -v n="$3" '{ c[$2 "." substr($3, 1, 2)]++ } END { for (h in c) if (c[h] == n) print h }' | sort
}

manifest=$tmp/MANIFEST
: >"$manifest"

# get NAME URL: download URL to $out/NAME unless present (bounded retries),
# then record it in the manifest.
get() {
	name=$1 url=$2
	if [ -s "$out/$name" ]; then
		echo "fetch-mrt: $name present, not downloaded"
	else
		tries=0
		until curl -fsS --retry 3 --max-time 3600 -o "$out/$name.part" "$url"; do
			tries=$((tries + 1))
			if [ "$tries" -ge 3 ]; then
				echo "fetch-mrt: $url: failed after $tries attempts" >&2
				exit 1
			fi
			sleep 10
		done
		mv "$out/$name.part" "$out/$name"
	fi
	size=$(wc -c <"$out/$name" | tr -d ' ')
	sum=$(sha256 "$out/$name")
	printf '%s %s %s %s\n' "$name" "$url" "$size" "$sum" >>"$manifest"
	printf '%-36s %12s bytes  %s\n' "$name" "$size" "$url"
}

# (newest runs in an assignment so that its failure stops the script.)
found=$(newest "$RIS/@" 'bview\.[0-9]*\.[0-9]*\.gz')
set -- $found
get "rrc00-$2" "$RIS/$1/$2"
found=$(newest "$RV2/@/RIBS" 'rib\.[0-9]*\.[0-9]*\.bz2')
set -- $found
get "rv2-$2" "$RV2/$1/RIBS/$2"
found=$(newest "$RV6/@/RIBS" 'rib\.[0-9]*\.[0-9]*\.bz2')
set -- $found
get "rv6-$2" "$RV6/$1/RIBS/$2"

# The newest hour both archives hold in full.
hours "$RIS/@" 'updates\.[0-9]*\.[0-9]*\.gz' 12 >"$tmp/ris.hours"
hours "$RV2/@/UPDATES" 'updates\.[0-9]*\.[0-9]*\.bz2' 4 >"$tmp/rv2.hours"
hour=$(comm -12 "$tmp/ris.hours" "$tmp/rv2.hours" | tail -1)
if [ -z "$hour" ]; then
	echo "fetch-mrt: no hour with every rrc00 and route-views2 update file" >&2
	exit 1
fi
day=${hour%.*} hh=${hour#*.}
month=$(echo "$day" | sed 's/^\(....\)\(..\).*/\1.\2/')
for mm in 00 05 10 15 20 25 30 35 40 45 50 55; do
	get "rrc00-updates.$day.$hh$mm.gz" "$RIS/$month/updates.$day.$hh$mm.gz"
done
for mm in 00 15 30 45; do
	get "rv2-updates.$day.$hh$mm.bz2" "$RV2/$month/UPDATES/updates.$day.$hh$mm.bz2"
done

mv "$manifest" "$out/MANIFEST"
echo "fetch-mrt: pinned in $out/MANIFEST"
