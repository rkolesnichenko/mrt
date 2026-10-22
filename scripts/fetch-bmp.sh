#!/bin/sh
# Captures OpenBMP frames from a Kafka topic into .data/realdata/bmp
# (git-ignored; never committed) for the opt-in TestRealDataBMP (spec §9.5):
#
#   MRT_BMP_CAPTURE_OK=1 scripts/fetch-bmp.sh BROKER TOPIC PARTITION OFFSET COUNT
#
# It refuses to run unless MRT_BMP_CAPTURE_OK=1, which only the user sets: the
# licence terms of the streams are unknown and nothing is captured until the
# user rules (spec §5.4, §9.5). It needs kcat (brew install kcat).
#
# It reads COUNT messages of TOPIC from PARTITION, starting at OFFSET, with
#   kcat -C -b BROKER -t TOPIC -p PARTITION -o OFFSET -c COUNT -e -D ''
# (no delimiter: every Kafka message is one self-delimiting OBMP frame) into
# .data/realdata/bmp/TOPIC.PARTITION.OFFSET.UTC.obmp (an existing file is
# never overwritten), and appends one line to .data/realdata/bmp/MANIFEST:
#
#   name broker topic partition offset count size sha256
#
# COUNT is the number asked for; kcat ends early if the partition holds fewer,
# and TestRealDataBMP then fails on the frame count.
set -u
cd "$(dirname "$0")/.."

if [ "${MRT_BMP_CAPTURE_OK:-}" != 1 ]; then
	echo "fetch-bmp: the user has not approved a capture (spec §9.5)" >&2
	exit 2
fi
if ! command -v kcat >/dev/null; then
	echo "fetch-bmp: kcat is not installed (brew install kcat)" >&2
	exit 2
fi
if [ $# -ne 5 ]; then
	echo "usage: MRT_BMP_CAPTURE_OK=1 scripts/fetch-bmp.sh BROKER TOPIC PARTITION OFFSET COUNT" >&2
	exit 2
fi
broker=$1 topic=$2 partition=$3 offset=$4 count=$5
case $broker in
'' | *[!A-Za-z0-9.:_-]*)
	echo "fetch-bmp: BROKER '$broker' is not host:port (characters A-Za-z0-9.:_-)" >&2
	exit 2
	;;
esac
for v in "$partition" "$offset"; do
	case $v in
	'' | *[!0-9]*)
		echo "fetch-bmp: PARTITION and OFFSET must be digits, got '$v'" >&2
		exit 2
		;;
	esac
done
case $count in
'' | *[!0-9]* | 0)
	echo "fetch-bmp: COUNT must be a positive integer, got '$count'" >&2
	exit 2
	;;
esac
case $topic in
*[!A-Za-z0-9._-]*)
	echo "fetch-bmp: TOPIC '$topic' has characters outside A-Za-z0-9._-" >&2
	exit 2
	;;
esac

if command -v shasum >/dev/null; then
	sha256() { shasum -a 256 "$1" | cut -d' ' -f1; }
elif command -v sha256sum >/dev/null; then
	sha256() { sha256sum "$1" | cut -d' ' -f1; }
else
	echo "fetch-bmp: neither shasum nor sha256sum is installed" >&2
	exit 1
fi

out=.data/realdata/bmp
mkdir -p "$out"
name=$topic.$partition.$offset.$(date -u +%Y%m%dT%H%M%SZ).obmp
if [ -e "$out/$name" ] || [ -e "$out/$name.part" ]; then
	echo "fetch-bmp: $out/$name exists; not overwriting" >&2
	exit 1
fi
trap 'rm -f "$out/$name.part"' EXIT
trap 'exit 130' INT TERM

kcat -C -b "$broker" -t "$topic" -p "$partition" -o "$offset" -c "$count" -e -D '' >"$out/$name.part"
status=$?
if [ "$status" -ne 0 ]; then
	echo "fetch-bmp: kcat exited $status" >&2
	exit 1
fi
if [ ! -s "$out/$name.part" ]; then
	echo "fetch-bmp: kcat wrote nothing" >&2
	exit 1
fi
mv "$out/$name.part" "$out/$name"

size=$(wc -c <"$out/$name" | tr -d ' ')
sum=$(sha256 "$out/$name")
printf '%s %s %s %s %s %s %s %s\n' "$name" "$broker" "$topic" "$partition" "$offset" "$count" "$size" "$sum" >>"$out/MANIFEST"
printf '%-48s %12s bytes  %s\n' "$name" "$size" "$sum"
echo "fetch-bmp: pinned in $out/MANIFEST"
