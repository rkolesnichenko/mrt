package mrtdump

import (
	"bytes"
	"compress/gzip"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const synthetic = "../../testdata/synthetic/"

// run calls Run and returns its exit status, stdout and stderr.
func run(t *testing.T, stdin []byte, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := Run(args, bytes.NewReader(stdin), &out, &errb)
	return code, out.String(), errb.String()
}

// runBytes writes b to a temporary file and runs mrtdump over it.
func runBytes(t *testing.T, b []byte, flags ...string) (int, string, string) {
	t.Helper()
	f := filepath.Join(t.TempDir(), "in.mrt")
	if err := os.WriteFile(f, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return run(t, nil, append(flags, f)...)
}

// lines joins want into one output text, a newline after each line.
func lines(want ...string) string { return strings.Join(want, "\n") + "\n" }

func checkOut(t *testing.T, what, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("%s:\ngot:\n%s\nwant:\n%s", what, got, want)
	}
}

func TestMrtdumpRefusesUnknownFlags(t *testing.T) {
	for _, args := range [][]string{{"-H", "x"}, {"-t", "change", "x"}, {"-p", "x"}, {"-v", "x"}} {
		code, out, errs := run(t, nil, args...)
		if code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
		if out != "" {
			t.Errorf("%v: stdout %q, want nothing", args, out)
		}
		if !strings.Contains(errs, args[0]) {
			t.Errorf("%v: stderr does not name %s:\n%s", args, args[0], errs)
		}
	}
	if code, _, errs := run(t, nil, "-m"); code != 2 || !strings.Contains(errs, "no input file") {
		t.Errorf("no file: exit %d, stderr %q; want 2 and a message", code, errs)
	}
}

// TestMrtdumpCommandLineDivergences pins two command-line differences from
// bgpdump (testdata/bgpdump/divergences.md): clustered flags are refused,
// and every argument after the first file name is a file name.
func TestMrtdumpCommandLineDivergences(t *testing.T) {
	code, out, errs := run(t, nil, "-ml", synthetic+"nh32.mrt")
	if code != 2 || out != "" || !strings.Contains(errs, "-ml") {
		t.Errorf("-ml: exit %d, stdout %q, stderr %q; want 2, nothing, a message naming -ml", code, out, errs)
	}
	code, out, errs = run(t, nil, synthetic+"nh32.mrt", "-l")
	if code != 3 || !strings.Contains(errs, "open -l") || !strings.Contains(out, "2001:db8:400::/48") {
		t.Errorf("FILE -l: exit %d, stderr %q; want 3, the file printed and -l opened as a file", code, errs)
	}
}

func TestMrtdumpMissingFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent.mrt")
	code, out, errs := run(t, nil, "-m", missing, synthetic+"nh32.mrt")
	if code != 3 {
		t.Errorf("exit %d, want 3", code)
	}
	if !strings.Contains(errs, missing) {
		t.Errorf("stderr does not name the file:\n%s", errs)
	}
	// The other file is still read.
	if !strings.Contains(out, "2001:db8:400::/48") {
		t.Errorf("the readable file was not printed:\n%s", out)
	}
}

// A stream that breaks while being read is status 3; one whose last record is
// cut short is a malformed record, status 1. Both keep what came before.
func TestMrtdumpReadErrors(t *testing.T) {
	raw, err := os.ReadFile(synthetic + "nh32.mrt")
	if err != nil {
		t.Fatal(err)
	}
	var gz bytes.Buffer
	w := gzip.NewWriter(&gz)
	w.Write(raw)
	w.Close()
	corrupt := gz.Bytes()
	corrupt[len(corrupt)-5] ^= 0xFF // the gzip trailer's CRC no longer matches
	code, out, errs := runBytes(t, corrupt, "-m")
	if code != 3 || !strings.Contains(errs, "in.mrt") {
		t.Errorf("corrupt gzip: exit %d, stderr %q; want 3 naming the file", code, errs)
	}
	if !strings.Contains(out, "2001:db8:400::/48") {
		t.Errorf("corrupt gzip: records before the failure were not printed:\n%s", out)
	}

	code, out, errs = runBytes(t, raw[:len(raw)-3], "-m")
	if code != 1 || !strings.Contains(errs, "record 3 at offset") || !strings.Contains(errs, "truncated") {
		t.Errorf("cut short: exit %d, stderr %q; want 1 naming record 3", code, errs)
	}
	if !strings.HasPrefix(out, "TABLE_DUMP2|") || strings.Count(out, "\n") != 1 {
		t.Errorf("cut short: want the RIB line only, got:\n%s", out)
	}
}

// TestMrtdumpTruncatedMessageExit1: messages cut short of their header's
// length print what their whole fields hold, exactly as bgpdump prints them
// (lines from bgpdump, TZ=UTC -m -l -u), and each is reported, exit 1
// (Ruling R17a; bgpdump exits 0).
func TestMrtdumpTruncatedMessageExit1(t *testing.T) {
	code, out, errs := run(t, nil, "-m", "-l", "-u", synthetic+"short-message.mrt")
	if code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	a := func(ts, prefix string) string {
		return "BGP4MP|" + ts + "|A|192.0.2.10|64500|" + prefix + "|64500 64501|IGP|192.0.2.10|0|0|||NAG|||"
	}
	want := lines(
		a("1700000000", "198.51.100.0/24"), a("1700000000", "198.51.101.0/24"), a("1700000000", "198.51.102.0/24"),
		a("1700000001", "198.51.103.0/24"), a("1700000001", "198.51.104.0/24"), a("1700000001", "198.51.105.0/24"),
		a("1700000002", "198.51.106.0/24"), a("1700000002", "198.51.107.0/24"),
		"BGP4MP|1700000003|W|192.0.2.10|64500|198.51.112.0/24",
		"BGP4MP|1700000003|W|192.0.2.10|64500|198.51.113.0/24",
		"BGP4MP|1700000004|A|192.0.2.10|64500|2001:db8:600::/48|64500 64501|IGP|2001:db8::10|0|0|||NAG|||",
	)
	checkOut(t, "stdout", out, want)
	for _, s := range []string{
		"record 2 at offset 83: bgp/message-truncated: message cut at 55 of its 58 bytes",
		"record 3 at offset 166: bgp/message-truncated: message cut at 54 of its 55 bytes",
		"record 4 at offset 248: bgp/message-truncated: message cut at 31 of its 35 bytes",
		"record 5 at offset 307: bgp/message-truncated: message cut at 71 of its 74 bytes",
	} {
		if !strings.Contains(errs, s) {
			t.Errorf("stderr lacks %q:\n%s", s, errs)
		}
	}
	if n := strings.Count(errs, "\n"); n != 4 {
		t.Errorf("%d stderr lines, want 4:\n%s", n, errs)
	}
}

func TestMrtdumpMalformedExit1(t *testing.T) {
	code, out, errs := run(t, nil, "-m", "-l", "-u", synthetic+"attrs-malformed.mrt")
	if code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	want := lines(
		// bgpdump prints MED 256 here: it reads the 3-byte MED as a 4-byte
		// one. Its line with the malformed MED left out (0) is this one.
		"BGP4MP|1700000000|A|192.0.2.10|64500|198.51.100.0/32|64500 64501|IGP|192.0.2.10|0|0|||NAG|||",
		// bgpdump aborts on the duplicate ORIGIN (assertion), printing
		// nothing more. The first ORIGIN wins (RFC 7606); the line is
		// bgpdump's for the same UPDATE without the second ORIGIN.
		"BGP4MP|1700000001|A|192.0.2.10|64500|198.51.100.1/32|64500 64501|IGP|192.0.2.10|0|0|||NAG|||",
		// bgpdump, this record alone (it warns "attribute is truncated").
		"BGP4MP|1700000002|A|192.0.2.10|64500|198.51.100.2/32||IGP|192.0.2.10|0|0|||NAG|||",
		// bgpdump, this record alone.
		"BGP4MP|1700000003|A|192.0.2.10|64500|198.51.100.3/32|64500 64501|IGP|192.0.2.10|0|50|||NAG|||",
	)
	checkOut(t, "stdout", out, want)
	for _, s := range []string{
		"attrs-malformed.mrt: record 1 at offset 0: bgp/attr-length",
		"record 2 at offset 90: bgp/attr-duplicate",
		"record 3 at offset 178: bgp/attr-truncated",
	} {
		if !strings.Contains(errs, s) {
			t.Errorf("stderr lacks %q:\n%s", s, errs)
		}
	}
	if n := strings.Count(errs, "\n"); n != 3 {
		t.Errorf("stderr has %d lines, want 3 (one per diagnostic):\n%s", n, errs)
	}
}

// Every expected text below is what bgpdump (caf2cb07, TZ=UTC) printed for
// the same bytes with the same flags, unless a comment says otherwise.
func TestMrtdumpLinePrefixes(t *testing.T) {
	tests := []struct {
		name  string
		file  string // under testdata/synthetic, or
		bytes []byte // built in cases_test.go
		flags []string
		want  string
	}{
		{name: "TABLE_DUMP2_AP", file: "addpath-rib-v4v6.mrt", flags: []string{"-m", "-l", "-u"}, want: lines(
			"TABLE_DUMP2_AP|1700000000|B|192.0.2.10|64500|198.51.100.0/24|1|64500 64501|IGP|192.0.2.10|0|0|||NAG|||",
			"TABLE_DUMP2_AP|1700000000|B|192.0.2.10|64500|198.51.100.0/24|2|64500 64502 64501|IGP|192.0.2.10|0|0|||NAG|||",
			"TABLE_DUMP2_AP|1700000000|B|2001:db8::11|4200000001|2001:db8:100::/48|1|4200000001 64501|IGP|2001:db8::11|0|0|||NAG|||",
			"TABLE_DUMP2_AP|1700000000|B|2001:db8::11|4200000001|2001:db8:100::/48|2|4200000001 64502 64501|IGP|2001:db8::11|0|0|||NAG|||",
		)},
		{name: "TABLE_DUMP2_AP -M", file: "addpath-rib-v4v6.mrt", flags: []string{"-M", "-l", "-u"}, want: lines(
			"TABLE_DUMP2_AP|11/14/23 22:13:20|B|192.0.2.10|64500|198.51.100.0/24|1|64500 64501|IGP",
			"TABLE_DUMP2_AP|11/14/23 22:13:20|B|192.0.2.10|64500|198.51.100.0/24|2|64500 64502 64501|IGP",
			"TABLE_DUMP2_AP|11/14/23 22:13:20|B|2001:db8::11|4200000001|2001:db8:100::/48|1|4200000001 64501|IGP",
			"TABLE_DUMP2_AP|11/14/23 22:13:20|B|2001:db8::11|4200000001|2001:db8:100::/48|2|4200000001 64502 64501|IGP",
		)},
		{name: "TABLE_DUMP2", file: "concat-peer-tables.mrt", flags: []string{"-m", "-l", "-u"}, want: lines(
			"TABLE_DUMP2|1700000000|B|192.0.2.10|64500|198.51.100.0/24|64500 64501|IGP|192.0.2.10|0|0|||NAG|||",
			"TABLE_DUMP2|1700000001|B|192.0.2.99|64999|198.51.100.0/24|64999 64501|IGP|192.0.2.99|0|0|||NAG|||",
		)},
		{name: "BGP4MP_AP, BGP4MP_ET_AP", file: "addpath-updates.mrt", flags: []string{"-m", "-l", "-u"}, want: lines(
			"BGP4MP_AP|1700000001|A|192.0.2.10|64500|203.0.113.0/24|1|64500 64501|IGP|192.0.2.10|0|0|||NAG|||",
			"BGP4MP_AP|1700000001|A|192.0.2.10|64500|203.0.113.0/24|2|64500 64501|IGP|192.0.2.10|0|0|||NAG|||",
			"BGP4MP_ET_AP|1700000002.123456|A|2001:db8::11|4200000001|2001:db8:200::/48|7|4200000001 64501|IGP|2001:db8::11|0|0|||NAG|||",
			"BGP4MP_ET_AP|1700000002.123456|A|2001:db8::11|4200000001|2001:db8:200::/48|8|4200000001 64501|IGP|2001:db8::11|0|0|||NAG|||",
			"BGP4MP_AP|1700000003|W|192.0.2.10|64500|203.0.113.0/24|1",
		)},
		{name: "BGP4MP_ET_AP -M", file: "addpath-updates.mrt", flags: []string{"-M"}, want: lines(
			"BGP4MP_AP|11/14/23 22:13:21|A|192.0.2.10|64500|203.0.113.0/24|1|64500 64501|IGP",
			"BGP4MP_AP|11/14/23 22:13:21|A|192.0.2.10|64500|203.0.113.0/24|2|64500 64501|IGP",
			"BGP4MP_ET_AP|11/14/23 22:13:22.123456|A|2001:db8::11|4200000001|2001:db8:200::/48|7|4200000001 64501|IGP",
			"BGP4MP_ET_AP|11/14/23 22:13:22.123456|A|2001:db8::11|4200000001|2001:db8:200::/48|8|4200000001 64501|IGP",
			"BGP4MP_AP|11/14/23 22:13:23|W|192.0.2.10|64500|203.0.113.0/24|1",
		)},
		{name: "BGP4MP without -l -u", file: "addpath-updates.mrt", flags: []string{"-m"}, want: lines(
			"BGP4MP_AP|1700000001|A|192.0.2.10|64500|203.0.113.0/24|1|64500 64501|IGP|192.0.2.10|0|0||NAG||",
			"BGP4MP_AP|1700000001|A|192.0.2.10|64500|203.0.113.0/24|2|64500 64501|IGP|192.0.2.10|0|0||NAG||",
			"BGP4MP_ET_AP|1700000002.123456|A|2001:db8::11|4200000001|2001:db8:200::/48|7|4200000001 64501|IGP|2001:db8::11|0|0||NAG||",
			"BGP4MP_ET_AP|1700000002.123456|A|2001:db8::11|4200000001|2001:db8:200::/48|8|4200000001 64501|IGP|2001:db8::11|0|0||NAG||",
			"BGP4MP_AP|1700000003|W|192.0.2.10|64500|203.0.113.0/24|1",
		)},
		{name: "32-byte next hop", file: "nh32.mrt", flags: []string{"-m", "-l", "-u"}, want: lines(
			"TABLE_DUMP2|1700000000|B|2001:db8::11|4200000001|2001:db8:400::/48|4200000001 64501|IGP|2001:db8::11|0|0|||NAG|||",
			"BGP4MP|1700000001|A|2001:db8::11|4200000001|2001:db8:400::/48|4200000001 64501|IGP|2001:db8::11|0|0|||NAG|||",
		)},
		{name: "AS4 merge", file: "as4-merge.mrt", flags: []string{"-m", "-l", "-u"}, want: lines(
			"BGP4MP|1700000000|A|192.0.2.10|64500|198.51.100.0/24|64500 196608 196609|IGP|192.0.2.10|0|0|||NAG|||",
			"BGP4MP|1700000001|A|192.0.2.10|64500|198.51.101.0/24|64500 23456|IGP|192.0.2.10|0|0|||NAG|||",
			"BGP4MP|1700000002|A|192.0.2.10|64500|198.51.102.0/24|64500 23456|IGP|192.0.2.10|0|0|||NAG|64501 192.0.2.77||",
			"BGP4MP|1700000003|A|192.0.2.10|64500|198.51.103.0/24|64500 196608|IGP|192.0.2.10|0|0|||NAG|196610 192.0.2.77||",
			"BGP4MP|1700000004|A|192.0.2.10|64500|198.51.104.0/24|64500 196608 {196609,64502}|IGP|192.0.2.10|0|0|||NAG|||",
		)},
		{name: "OTC, large and extended communities -l -u", file: "otc-large.mrt", flags: []string{"-m", "-l", "-u"}, want: lines(
			"BGP4MP|1700000000|A|192.0.2.10|64500|203.0.113.0/32|64500 64501|IGP|192.0.2.10|0|0|||NAG||23:c0:0000fbf4|",
			"BGP4MP|1700000001|A|192.0.2.10|64500|203.0.113.1/32|64500 64501|IGP|192.0.2.10|0|0|65535:1 64500:100 no-export|64500:1:2 4200000001:0:4294967295|NAG|||",
			"BGP4MP|1700000002|A|192.0.2.10|64500|203.0.113.2/32|64500 64501|IGP|192.0.2.10|0|0|||NAG||10:c0:0002fbf4000000640103c00002010007 23:c0:fa56ea01|",
			"BGP4MP|1700000003|A|192.0.2.10|64500|203.0.113.3/32|64500 64501|IGP|192.0.2.10|0|0|65000:1|64500:10:20|NAG||10:c0:0002fbf400000064 23:c0:0000fbf5|",
		)},
		{name: "-u alone", file: "otc-large.mrt", flags: []string{"-m", "-u"}, want: lines(
			"BGP4MP|1700000000|A|192.0.2.10|64500|203.0.113.0/32|64500 64501|IGP|192.0.2.10|0|0||NAG||23:c0:0000fbf4|",
			"BGP4MP|1700000001|A|192.0.2.10|64500|203.0.113.1/32|64500 64501|IGP|192.0.2.10|0|0|65535:1 64500:100 no-export|NAG|||",
			"BGP4MP|1700000002|A|192.0.2.10|64500|203.0.113.2/32|64500 64501|IGP|192.0.2.10|0|0||NAG||10:c0:0002fbf4000000640103c00002010007 23:c0:fa56ea01|",
			"BGP4MP|1700000003|A|192.0.2.10|64500|203.0.113.3/32|64500 64501|IGP|192.0.2.10|0|0|65000:1|NAG||10:c0:0002fbf400000064 23:c0:0000fbf5|",
		)},
		{name: "-l alone", file: "otc-large.mrt", flags: []string{"-m", "-l"}, want: lines(
			"BGP4MP|1700000000|A|192.0.2.10|64500|203.0.113.0/32|64500 64501|IGP|192.0.2.10|0|0|||NAG||",
			"BGP4MP|1700000001|A|192.0.2.10|64500|203.0.113.1/32|64500 64501|IGP|192.0.2.10|0|0|65535:1 64500:100 no-export|64500:1:2 4200000001:0:4294967295|NAG||",
			"BGP4MP|1700000002|A|192.0.2.10|64500|203.0.113.2/32|64500 64501|IGP|192.0.2.10|0|0|||NAG||",
			"BGP4MP|1700000003|A|192.0.2.10|64500|203.0.113.3/32|64500 64501|IGP|192.0.2.10|0|0|65000:1|64500:10:20|NAG||",
		)},
		{name: "STATE", bytes: stateFile(), flags: []string{"-m", "-l", "-u"}, want: lines(
			"BGP4MP|1700000000|STATE|192.0.2.10|64500|1|2",
			"BGP4MP|1700000001|STATE|2001:db8::11|4200000001|5|6",
			"BGP4MP_ET|1700000002.000005|STATE|192.0.2.10|64500|6|1",
			"BGP4MP|1700000003|STATE|192.0.2.10|64500|1|9",
		)},
		{name: "STATE -M", bytes: stateFile(), flags: []string{"-M"}, want: lines(
			"BGP4MP|11/14/23 22:13:20|STATE|192.0.2.10|64500|1|2",
			"BGP4MP|11/14/23 22:13:21|STATE|2001:db8::11|4200000001|5|6",
			"BGP4MP_ET|11/14/23 22:13:22.000005|STATE|192.0.2.10|64500|6|1",
			"BGP4MP|11/14/23 22:13:23|STATE|192.0.2.10|64500|1|9",
		)},
		{name: "TABLE_DUMP", bytes: tableDumpFile(), flags: []string{"-m", "-l", "-u"}, want: lines(
			"TABLE_DUMP|1700000000|B|192.0.2.10|64500|198.51.100.0/24|64500 64501|IGP|192.0.2.10|0|0|64500:1||NAG|||",
			"TABLE_DUMP|1700000001|B|2001:db8::11|64500|2001:db8:100::/48|64500|IGP|2001:db8::11|0|0|||NAG|||",
		)},
		{name: "TABLE_DUMP -M", bytes: tableDumpFile(), flags: []string{"-M"}, want: lines(
			"TABLE_DUMP|11/14/23 22:13:20|B|192.0.2.10|64500|198.51.100.0/24|64500 64501|IGP",
			"TABLE_DUMP|11/14/23 22:13:21|B|2001:db8::11|64500|2001:db8:100::/48|64500|IGP",
		)},
		{name: "BGP4MP, BGP4MP_LOCAL", bytes: pathsFile(), flags: []string{"-m", "-l", "-u"}, want: lines(
			"BGP4MP|1700000000|A|192.0.2.10|64500|198.51.100.0/24||IGP|192.0.2.10|100|10|||AG|64500 192.0.2.77||",
			"BGP4MP|1700000001|A|192.0.2.10|64500|198.51.100.1/32||IGP|192.0.2.10|0|0|||NAG|||",
			"BGP4MP|1700000002|A|192.0.2.10|64500|198.51.100.2/32|1 2 {3,4}|IGP|192.0.2.10|0|0|||NAG|||",
			"BGP4MP|1700000003|A|192.0.2.10|64500|198.51.100.3/32|(65001 65002) [65003,65004] 64500|INCOMPLETE|192.0.2.10|0|0|||NAG|||",
			// bgpdump prints LOCAL_PREF and MED here with %d, as
			// -1294967296|-294967296; mrtdump prints them unsigned, as
			// every other bgpdump line does.
			"BGP4MP|1700000004|A|192.0.2.10|64500|198.51.100.4/32|64500|EGP|192.0.2.20|3000000000|4000000000|||NAG|||",
			"BGP4MP|1700000005|W|192.0.2.10|64500|198.51.100.5/32",
			"BGP4MP|1700000005|W|192.0.2.10|64500|198.51.100.6/32",
			"BGP4MP|1700000005|W|192.0.2.10|64500|2001:db8:500::/48",
			"BGP4MP|1700000005|A|192.0.2.10|64500|198.51.100.7/32|64500|IGP|192.0.2.10|0|0|||NAG|||",
			"BGP4MP|1700000005|A|192.0.2.10|64500|198.51.100.8/32|64500|IGP|192.0.2.10|0|0|||NAG|||",
			"BGP4MP_LOCAL|1700000006|A|2001:db8::11|4200000001|2001:db8:600::/48|64496|IGP|2001:db8::1|0|0|||NAG|||",
			"BGP4MP|1700000007|A|2001:db8::11|64500|2001:db8:700::/48|64500|IGP|2001:db8::11|0|0|||NAG|||",
			"BGP4MP|1700000009|A|192.0.2.10|64500|198.51.100.9/32|64500|IGP|192.0.2.10|0|0|no-export no-advertise local-AS 65535:65284 0:0||NAG|||",
		)},
		{name: "BGP4MP -M", bytes: pathsFile(), flags: []string{"-M", "-l", "-u"}, want: lines(
			"BGP4MP|11/14/23 22:13:20|A|192.0.2.10|64500|198.51.100.0/24||IGP",
			"BGP4MP|11/14/23 22:13:21|A|192.0.2.10|64500|198.51.100.1/32||IGP",
			"BGP4MP|11/14/23 22:13:22|A|192.0.2.10|64500|198.51.100.2/32|1 2 {3,4}|IGP",
			"BGP4MP|11/14/23 22:13:23|A|192.0.2.10|64500|198.51.100.3/32|(65001 65002) [65003,65004] 64500|INCOMPLETE",
			"BGP4MP|11/14/23 22:13:24|A|192.0.2.10|64500|198.51.100.4/32|64500|EGP",
			"BGP4MP|11/14/23 22:13:25|W|192.0.2.10|64500|198.51.100.5/32",
			"BGP4MP|11/14/23 22:13:25|W|192.0.2.10|64500|198.51.100.6/32",
			"BGP4MP|11/14/23 22:13:25|W|192.0.2.10|64500|2001:db8:500::/48",
			"BGP4MP|11/14/23 22:13:25|A|192.0.2.10|64500|198.51.100.7/32|64500|IGP",
			"BGP4MP|11/14/23 22:13:25|A|192.0.2.10|64500|198.51.100.8/32|64500|IGP",
			"BGP4MP_LOCAL|11/14/23 22:13:26|A|2001:db8::11|4200000001|2001:db8:600::/48|64496|IGP",
			"BGP4MP|11/14/23 22:13:27|A|2001:db8::11|64500|2001:db8:700::/48|64500|IGP",
			"BGP4MP|11/14/23 22:13:29|A|192.0.2.10|64500|198.51.100.9/32|64500|IGP",
		)},
		{name: "RIB_GENERIC", file: "rib-generic.mrt", flags: []string{"-m", "-l", "-u"}, want: lines(
			// bgpdump prints nothing for RIB_GENERIC (its decoder is
			// commented out). These lines are in its TABLE_DUMP2 format;
			// the VPNv4 entry has no prefix to print.
			"TABLE_DUMP2|1700000000|B|192.0.2.10|64500|192.0.2.0/25|64500 64501|IGP|192.0.2.10|0|0|||NAG|||",
			"TABLE_DUMP2_AP|1700000000|B|2001:db8::11|4200000001|2001:db8:300::/48|5|4200000001|IGP|2001:db8::11|0|0|||NAG|||",
		)},
		{name: "TABLE_DUMP2 empty path, no NEXT_HOP, multicast", bytes: ribMiscFile(), flags: []string{"-m", "-l", "-u"}, want: lines(
			"TABLE_DUMP2|1700000000|B|192.0.2.10|64500|198.51.100.0/24||IGP|192.0.2.10|100|0|||NAG|||",
			"TABLE_DUMP2|1700000000|B|192.0.2.10|64500|198.51.101.0/24|64500|IGP|255.255.255.255|0|0|||NAG|||",
			// bgpdump prints nothing for an IPv4 multicast RIB; this line
			// is in its TABLE_DUMP2 format.
			"TABLE_DUMP2|1700000000|B|192.0.2.10|64500|198.51.102.0/24|64500|IGP|192.0.2.10|0|0|||NAG|||",
		)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var code int
			var out, errs string
			if tc.file != "" {
				code, out, errs = run(t, nil, append(tc.flags, synthetic+tc.file)...)
			} else {
				code, out, errs = runBytes(t, tc.bytes, tc.flags...)
			}
			if code != 0 || errs != "" {
				t.Errorf("exit %d, stderr %q; want 0 and nothing", code, errs)
			}
			checkOut(t, "stdout", out, tc.want)
		})
	}
}

// bgpdump's words for attributes it cannot use: "! Error !" for an AS_PATH
// that does not parse, INCOMPLETE for an ORIGIN above 2, AG for any
// ATOMIC_AGGREGATE. Both lines are bgpdump's for the same bytes; mrtdump also
// reports the three attributes (exit 1).
func TestMrtdumpMalformedAttrsPrintAsBgpdump(t *testing.T) {
	code, out, errs := runBytes(t, oddAttrsFile(), "-m", "-l", "-u")
	checkOut(t, "-m -l -u", out, lines(
		"BGP4MP|1700000000|A|192.0.2.10|64500|198.51.100.0/24|! Error !|INCOMPLETE|192.0.2.10|0|0|||AG|||"))
	if code != 1 || strings.Count(errs, "\n") != 3 {
		t.Errorf("exit %d, stderr:\n%s\nwant 1 and three diagnostics", code, errs)
	}
	_, out, _ = runBytes(t, oddAttrsFile(), "-M")
	checkOut(t, "-M", out, lines("BGP4MP|11/14/23 22:13:20|A|192.0.2.10|64500|198.51.100.0/24|! Error !|INCOMPLETE"))
}

// Review Focus 5: an empty AS path is an empty field, not a missing one, and
// a path ending in an AS_SET keeps the set's braces. bgpdump printed both.
func TestMrtdumpEmptyPath(t *testing.T) {
	_, out, _ := runBytes(t, pathsFile(), "-m")
	got := strings.Split(out, "\n")
	for i, want := range map[int]string{
		0: "BGP4MP|1700000000|A|192.0.2.10|64500|198.51.100.0/24||IGP|192.0.2.10|100|10||AG|64500 192.0.2.77|",
		1: "BGP4MP|1700000001|A|192.0.2.10|64500|198.51.100.1/32||IGP|192.0.2.10|0|0||NAG||",
		2: "BGP4MP|1700000002|A|192.0.2.10|64500|198.51.100.2/32|1 2 {3,4}|IGP|192.0.2.10|0|0||NAG||",
	} {
		if i >= len(got) || got[i] != want {
			t.Errorf("line %d:\ngot  %q\nwant %q", i, got[min(i, len(got)-1)], want)
		}
	}
	_, out, _ = runBytes(t, ribMiscFile(), "-M")
	if want := "TABLE_DUMP2|11/14/23 22:13:20|B|192.0.2.10|64500|198.51.100.0/24||IGP\n"; !strings.HasPrefix(out, want) {
		t.Errorf("RIB entry with an empty path:\ngot  %q\nwant %q", out, want)
	}
}

func TestMrtdumpStdinAndGzip(t *testing.T) {
	raw, err := os.ReadFile(synthetic + "nh32.mrt")
	if err != nil {
		t.Fatal(err)
	}
	var gz bytes.Buffer
	w := gzip.NewWriter(&gz)
	w.Write(raw)
	w.Close()
	_, want, _ := run(t, nil, "-m", synthetic+"nh32.mrt")
	code, out, errs := run(t, gz.Bytes(), "-m", "-")
	if code != 0 || errs != "" {
		t.Errorf("exit %d, stderr %q", code, errs)
	}
	checkOut(t, "gzip on stdin", out, want)
	// Two files print one after the other.
	_, both, _ := run(t, nil, "-m", synthetic+"nh32.mrt", synthetic+"nh32.mrt")
	checkOut(t, "two files", both, want+want)
}

// The last of -m and -M wins, as with bgpdump's getopt; neither means -m.
func TestMrtdumpModeFlags(t *testing.T) {
	_, m, _ := run(t, nil, "-m", synthetic+"nh32.mrt")
	_, mm, _ := run(t, nil, "-M", synthetic+"nh32.mrt")
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{synthetic + "nh32.mrt"}, m},
		{[]string{"-M", "-m", synthetic + "nh32.mrt"}, m},
		{[]string{"-m", "-M", synthetic + "nh32.mrt"}, mm},
	} {
		_, out, _ := run(t, nil, tc.args...)
		checkOut(t, strings.Join(tc.args, " "), out, tc.want)
	}
}

// fmtIPv6 is bgpdump's fmt_ipv6 (inet_ntop.c): the longest run of zero
// groups is compressed even when it is a single group, the first of equal
// runs wins, and v4-mapped and v4-compatible addresses end in dotted quads.
// Each expected value was printed by bgpdump's fmt_ipv6 itself (inet_ntop.c
// at caf2cb07, compiled with a two-line driver).
func TestFmtIPv6(t *testing.T) {
	for in, want := range map[string]string{
		// bgpdump's own test_fmt_ip cases.
		"fe80::":                  "fe80::",
		"2001:db8::1":             "2001:db8::1",
		"::ffff:192.168.2.1":      "::ffff:192.168.2.1",
		"::192.168.1.2":           "::192.168.1.2",
		"2001:7f8:30::2:1:0:8447": "2001:7f8:30::2:1:0:8447",
		// A single zero group is compressed (Go's String keeps it).
		"2001:db8:0:1:1:1:1:1": "2001:db8::1:1:1:1:1",
		"1:0:0:1:0:0:1:1":      "1::1:0:0:1:1",
		"::":                   "::",
		"::1":                  "::1",
		"1::":                  "1::",
		"0:0:1::":              "0:0:1::",
		"2001:db8:1:2:3:4:5:6": "2001:db8:1:2:3:4:5:6",
		"::0.0.0.2":            "::0.0.0.2",
		"::ffff:0:0":           "::ffff:0.0.0.0",
	} {
		if got := fmtIPv6(mustAddr16(t, in)); got != want {
			t.Errorf("fmtIPv6(%s) = %s, want %s", in, got, want)
		}
	}
}

func mustAddr16(t *testing.T, s string) netip.Addr {
	t.Helper()
	a, err := netip.ParseAddr(s)
	if err != nil {
		t.Fatal(err)
	}
	return netip.AddrFrom16(a.As16())
}
