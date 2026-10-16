#!/bin/sh
# Builds the two differential oracles and installs them into PREFIX
# (default $HOME/.local/bin):
#
#   bgpdump        RIPE-NCC/bgpdump master at $BGPDUMP_COMMIT, built with
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
BGPDUMP_COMMIT=caf2cb07bc3cf06f1720c4cb274811599e9bda6a
BGPKIT_VERSION=0.22.0

PREFIX=${1:-$HOME/.local/bin}
mkdir -p "$PREFIX"

tmp=$(mktemp -d "${TMPDIR:-/tmp}/build-oracles.XXXXXX")
trap 'rm -rf "$tmp"' EXIT INT TERM

sha256() { shasum -a 256 "$1" | cut -d' ' -f1; }

stamp="$PREFIX/.bgpdump.stamp"
if [ -x "$PREFIX/bgpdump" ] && [ -f "$stamp" ] &&
	[ "$(cat "$stamp")" = "$BGPDUMP_COMMIT $(sha256 "$PREFIX/bgpdump")" ]; then
	echo "bgpdump: $BGPDUMP_COMMIT already installed in $PREFIX"
else
	for tool in autoconf automake make cc git; do
		command -v "$tool" >/dev/null || { echo "bgpdump: $tool is missing" >&2; exit 1; }
	done
	echo "bgpdump: building $BGPDUMP_COMMIT"
	git clone --quiet "$BGPDUMP_REPO" "$tmp/bgpdump"
	(
		cd "$tmp/bgpdump"
		git checkout --quiet "$BGPDUMP_COMMIT"
		./bootstrap.sh >"$tmp/bgpdump-build.log" 2>&1 &&
			./configure >>"$tmp/bgpdump-build.log" 2>&1 &&
			make >>"$tmp/bgpdump-build.log" 2>&1
	) || { cat "$tmp/bgpdump-build.log" >&2; echo "bgpdump: build failed" >&2; exit 1; }
	cp "$tmp/bgpdump/bgpdump" "$PREFIX/bgpdump"
	chmod 755 "$PREFIX/bgpdump"
	echo "$BGPDUMP_COMMIT $(sha256 "$PREFIX/bgpdump")" >"$stamp"
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
