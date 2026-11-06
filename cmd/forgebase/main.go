// Command forgebase is the command-line tool of Forgebase: the stock binary, which has no apps and
// reads its DocTypes from --doctypes DIR. An application builds its own binary from the root
// package (from P4).
//
// The commands are in internal/app; this file only hands over the process: its arguments, its
// environment and its standard streams.
package main

import (
	"os"

	"github.com/tvstore/forgebase-go"
	"github.com/tvstore/forgebase-go/internal/app"
)

func main() {
	os.Exit(app.Run(app.Options{
		Version: forgebase.Version,
		Args:    os.Args[1:],
		Getenv:  os.Getenv,
		Stdin:   os.Stdin,
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
	}))
}
