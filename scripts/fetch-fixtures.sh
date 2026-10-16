#!/bin/sh
# Rebuilds the real-data fixtures in testdata/mrt from RouteViews' archive
# (CC BY 4.0; see testdata/README.md for the terms and the attribution):
#
#   scripts/fetch-fixtures.sh
#
# Each source file is downloaded whole into $MRT_FIXTURE_CACHE (default
# .data/fixtures, git-ignored; kept for the next run), its sha256 taken, and a
# slice cut at record boundaries by internal/mrtcut and written gzip-compressed
# to testdata/mrt/NAME.gz. One line per fixture is printed for
# testdata/README.md: name, source URL, download date, the source's sha256, and
# the sha256 of the slice's decompressed MRT bytes (what the tests read; the
# .gz bytes depend on the Go version's compressor, the MRT bytes do not).
#
# RIPE RIS files are not used: RIPE NCC's terms do not permit redistributing
# them (testdata/README.md quotes the clauses).
set -eu
cd "$(dirname "$0")/.."

RV=https://archive.routeviews.org
cache=${MRT_FIXTURE_CACHE:-.data/fixtures}
out=testdata/mrt
mkdir -p "$cache" "$out"

if command -v shasum >/dev/null; then
	sha256() { shasum -a 256 | cut -d' ' -f1; }
elif command -v sha256sum >/dev/null; then
	sha256() { sha256sum | cut -d' ' -f1; }
else
	echo "fetch-fixtures: neither shasum nor sha256sum is installed" >&2
	exit 1
fi

tmp=$(mktemp -d "${TMPDIR:-/tmp}/fetch-fixtures.XXXXXX")
trap 'rm -rf "$tmp"' EXIT
trap 'exit 130' INT TERM
go build -o "$tmp/mrtcut" ./internal/mrtcut

today=$(date -u +%Y-%m-%d)

# fixture NAME URL MRTCUT-ARGS...
fixture() {
	name=$1 url=$2
	shift 2
	src="$cache/$(echo "$url" | sed 's|^https://||; s|/|_|g')"
	if [ ! -f "$src" ]; then
		curl -fsS --retry 3 --max-time 1800 -o "$src.part" "$url"
		mv "$src.part" "$src"
		echo "$today" >"$src.date"
	fi
	"$tmp/mrtcut" "$@" "$src" >"$out/$name.gz" 2>"$tmp/cut.log" || {
		cat "$tmp/cut.log" >&2
		exit 1
	}
	printf -- '- `%s.gz`: %s (mrtcut %s); downloaded %s; source sha256 %s; slice (decompressed) sha256 %s; %s\n' \
		"$name" "$url" "$*" "$(cat "$src.date")" "$(sha256 <"$src")" \
		"$(gzip -dc "$out/$name.gz" | sha256)" "$(cat "$tmp/cut.log" | sed 's/^mrtcut: [^:]*: //' | tr '\n' ' ')"
}

# BGP4MP_ET / MESSAGE_AS4 updates, as every RouteViews collector writes today.
fixture rv2-updates-et.20261015.1200 "$RV/bgpdata/2026.10/UPDATES/updates.20261015.1200.bz2" -first 10000
# The same from route-views6: IPv6 in MP_REACH_NLRI and MP_UNREACH_NLRI. A
# window of 560 records with 369 withdrawals and only 3 routes whose UPDATE
# also carries NEXT_HOP (bgpkit-parser then gives NEXT_HOP as their next hop;
# testdata/bgpdump/divergences.md K7 pins each one, so few is better).
fixture rv6-updates-et.20261015.1200 "$RV/route-views6/bgpdata/2026.10/UPDATES/updates.20261015.1200.bz2" -skip 71535 -first 560
# 2-byte BGP4MP_MESSAGE UPDATEs (some with AS4_PATH) beside MESSAGE_AS4 ones.
fixture rv2-updates-as2.20110601.1200 "$RV/bgpdata/2011.06/UPDATES/updates.20110601.1200.bz2" -first 12000
# TABLE_DUMP_V2: PEER_INDEX_TABLE and RIB_IPV4_UNICAST (many peers: few records).
fixture rv2-rib.20261015.1200 "$RV/bgpdata/2026.10/RIBS/rib.20261015.1200.bz2" -first 2000
# TABLE_DUMP_V2 RIB_IPV4_UNICAST from a collector with few peers: more records.
fixture rvwide-rib.20261015.1200 "$RV/route-views.wide/bgpdata/2026.10/RIBS/rib.20261015.1200.bz2" -first 5000
# TABLE_DUMP_V2 RIB_IPV6_UNICAST (route-views6 writes the full RFC 4760
# MP_REACH_NLRI form in RIB entries, not RFC 6396's abbreviated one).
fixture rv6-rib.20261015.1200 "$RV/route-views6/bgpdata/2026.10/RIBS/rib.20261015.1200.bz2" -first 3000
# Legacy TABLE_DUMP, IPv4 (written before TABLE_DUMP_V2).
fixture rv2-tabledump.20070601.0025 "$RV/bgpdata/2007.06/RIBS/rib.20070601.0025.bz2" -first 10000
# Legacy TABLE_DUMP, IPv6.
fixture rv6-tabledump.20070601.0123 "$RV/route-views6/bgpdata/2007.06/RIBS/rib.20070601.0123.bz2" -first 5000
