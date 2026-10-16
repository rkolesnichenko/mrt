// Command mrtdump prints MRT files one line per route, as bgpdump -m does:
//
//	mrtdump [-m | -M] [-l] [-u] FILE...
//
// See internal/mrtdump for the format and the exit statuses.
package main

import (
	"os"

	"github.com/rkolesnichenko/mrt/internal/mrtdump"
)

func main() { os.Exit(mrtdump.Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }
