#!/bin/sh
# Builds the two differential oracles and installs them into PREFIX
# (default $HOME/.local/bin):
#
#   bgpdump        RIPE-NCC/bgpdump master at $BGPDUMP_COMMIT (default caf2cb07;
#                  the environment may override it), built with
#                  autotools (./bootstrap.sh && ./configure && make). Needs
#                  autoconf and automake (macOS: brew install autoconf automake).
#   bgpkit-parser  the crate at $BGPKIT_VERSION with its "cli" feature, built
#                  by cargo install --locked into a temporary root.
#
#   scripts/build-oracles.sh [PREFIX]
#
# Idempotent: a tool already installed at the pinned version is skipped.
# bgpdump's own version string (PACKAGE_VERSION) does not name a commit, so
# the script writes PREFIX/.bgpdump.stamp holding the commit and the
# installed binary's sha256, and skips the build only while both match.
set -eu

BGPDUMP_REPO=https://github.com/RIPE-NCC/bgpdump
BGPDUMP_COMMIT=${BGPDUMP_COMMIT:-caf2cb07bc3cf06f1720c4cb274811599e9bda6a}
BGPKIT_VERSION=0.22.0

PREFIX=${1:-$HOME/.local/bin}
mkdir -p "$PREFIX"

tmp=$(mktemp -d "${TMPDIR:-/tmp}/build-oracles.XXXXXX")
trap 'rm -rf "$tmp"' EXIT
trap 'exit 130' INT TERM

if command -v shasum >/dev/null; then
	sha256() { shasum -a 256 "$1" | cut -d' ' -f1; }
elif command -v sha256sum >/dev/null; then
	sha256() { sha256sum "$1" | cut -d' ' -f1; }
else
	echo "build-oracles: neither shasum nor sha256sum is installed" >&2
	exit 1
fi

stamp="$PREFIX/.bgpdump.stamp"
if [ -x "$PREFIX/bgpdump" ] && [ -f "$stamp" ] &&
	[ "$(cat "$stamp")" = "$BGPDUMP_COMMIT $(sha256 "$PREFIX/bgpdump")" ]; then
	echo "bgpdump: $BGPDUMP_COMMIT already installed in $PREFIX"
else
	for tool in autoconf automake make cc git; do
		command -v "$tool" >/dev/null || { echo "bgpdump: $tool is missing" >&2; exit 1; }
	done
	echo "bgpdump: building $BGPDUMP_COMMIT"
	src="$tmp/bgpdump"
	log="$tmp/bgpdump-build.log"
	# Every step is chained with &&: set -e does not apply on the left of ||.
	git clone --quiet "$BGPDUMP_REPO" "$src" &&
		git -C "$src" checkout --quiet "$BGPDUMP_COMMIT" &&
		(cd "$src" && ./bootstrap.sh >"$log" 2>&1 && ./configure >>"$log" 2>&1 && make >>"$log" 2>&1) ||
		{ if [ -f "$log" ]; then cat "$log" >&2; fi; echo "bgpdump: checkout or build of $BGPDUMP_COMMIT failed" >&2; exit 1; }
	head=$(git -C "$src" rev-parse HEAD)
	if [ "$head" != "$BGPDUMP_COMMIT" ]; then
		echo "bgpdump: built $head, not $BGPDUMP_COMMIT; nothing installed" >&2
		exit 1
	fi
	cp "$src/bgpdump" "$PREFIX/bgpdump"
	chmod 755 "$PREFIX/bgpdump"
	echo "$head $(sha256 "$PREFIX/bgpdump")" >"$stamp"
	echo "bgpdump: installed $PREFIX/bgpdump"
fi

# bgpkit-parser --version prints "bgpkit-parser 0.22.0".
if [ -x "$PREFIX/bgpkit-parser" ] &&
	[ "$("$PREFIX/bgpkit-parser" --version 2>/dev/null)" = "bgpkit-parser $BGPKIT_VERSION" ]; then
	echo "bgpkit-parser: $BGPKIT_VERSION already installed in $PREFIX"
else
	command -v cargo >/dev/null || { echo "bgpkit-parser: cargo is missing" >&2; exit 1; }
	echo "bgpkit-parser: building $BGPKIT_VERSION"
	cargo install bgpkit-parser --version "$BGPKIT_VERSION" --features cli --locked \
		--root "$tmp/cargo" --quiet
	cp "$tmp/cargo/bin/bgpkit-parser" "$PREFIX/bgpkit-parser"
	chmod 755 "$PREFIX/bgpkit-parser"
	echo "bgpkit-parser: installed $PREFIX/bgpkit-parser"
fi
