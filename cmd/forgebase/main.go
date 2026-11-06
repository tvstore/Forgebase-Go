// Command forgebase is the command-line tool of Forgebase. In phase P0 it only prints its version;
// the full command set arrives in later phases.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/tvstore/forgebase-go"
)

const usage = "usage: forgebase --version\n"

// run executes the command line (without the program name) and returns the exit code.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && (args[0] == "--version" || args[0] == "-V") {
		fmt.Fprintf(stdout, "forgebase %s\n", forgebase.Version)
		return 0
	}
	fmt.Fprint(stderr, usage)
	return 2
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
