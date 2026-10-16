// Command mrtgen (re)writes the synthetic MRT fixtures.
//
//	go run ./internal/mrtgen/cmd/mrtgen -out testdata/synthetic
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/rkolesnichenko/mrt/internal/mrtgen"
)

func main() {
	out := flag.String("out", "testdata/synthetic", "directory to write the .mrt files to")
	flag.Parse()
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "mrtgen:", err)
		os.Exit(1)
	}
	for _, f := range mrtgen.Synthetic() {
		if err := os.WriteFile(filepath.Join(*out, f.Name), f.Data, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "mrtgen:", err)
			os.Exit(1)
		}
		fmt.Printf("%s: %d bytes\n", f.Name, len(f.Data))
	}
}
