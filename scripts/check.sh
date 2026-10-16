#!/bin/sh
# Full verification — what CI runs, and what to run before a commit:
#
#   scripts/check.sh                 # gofmt, build, vet, race tests, coverage,
#                                    # api surface, invariants
#   FUZZTIME=15s scripts/check.sh    # ... plus every fuzz target for 15s each
#                                    # (scripts/fuzz.sh, which CI runs as its own job)
#
# staticcheck and govulncheck run when installed (CI installs them).
# internal/compare is its own module and is run by hand, not here.
set -u
cd "$(dirname "$0")/.."
fail=0
step() { printf '\n== %s\n' "$*"; }
bad() { echo "FAIL: $*"; fail=1; }

# gofiles lists the Go files the next commit would hold — tracked or new, not
# ignored — so tool state and .data/ are never scanned.
gofiles() {
	git ls-files -z --cached --others --exclude-standard '*.go' |
		xargs -0 sh -c 'for f; do [ -f "$f" ] && printf "%s\0" "$f"; done' _
}

step gofmt
unformatted=$(gofiles | xargs -0 gofmt -l)
[ -z "$unformatted" ] || bad "not gofmt-clean: $unformatted"

step "build, vet, test -race"
(go build ./... && go vet ./... && go test -race -count=1 ./...) || bad "build, vet or test"

# Coverage is reported, not enforced. It runs without -race, which slows the
# hot loops of the tests about fourfold.
step "coverage"
out=$(go test -count=1 -cover ./... 2>&1) || { printf '%s\n' "$out"; bad "coverage"; out=; }
printf '%s\n' "$out" | grep 'coverage:'

step "no package imports net"
if go list -deps ./... | grep -qx net; then bad "a package imports net"; fi

step "go.mod has no require"
if grep -q '^require' go.mod; then bad "go.mod has a require line"; fi

step "api surface: api/ matches the public API, and the API conventions hold"
go run ./internal/apisurface -check ||
	bad "api surface (MRT_API_UPDATE=1 go run ./internal/apisurface -check rewrites api/; review the diff)"

bin=$(go env GOPATH)/bin
for tool in staticcheck govulncheck; do
	cmd=$(command -v "$tool" || true)
	[ -n "$cmd" ] || { [ -x "$bin/$tool" ] && cmd="$bin/$tool"; }
	if [ -z "$cmd" ]; then
		step "$tool: not installed, skipped"
		continue
	fi
	step "$tool"
	"$cmd" ./... || bad "$tool"
done

if [ -n "${FUZZTIME:-}" ]; then
	scripts/fuzz.sh || bad "fuzz"
fi

echo
if [ "$fail" -ne 0 ]; then echo "check: FAILED"; exit 1; fi
echo "check: ok"
