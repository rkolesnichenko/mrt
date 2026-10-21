package bzip2

import (
	"os"
	"testing"
)

// FuzzBzip2 checks the copy against compress/bzip2 on arbitrary bytes: the
// same bytes up to the first error, and an error exactly when it errs.
func FuzzBzip2(f *testing.F) {
	for _, in := range inputs(f) {
		if len(in) <= 4096 {
			f.Add(in)
		}
	}
	bench, err := os.ReadFile(benchFile)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(bench[:4096])
	f.Add([]byte("BZh9"))
	f.Fuzz(func(t *testing.T, in []byte) {
		compareWithStdlib(t, in, 7)
	})
}
