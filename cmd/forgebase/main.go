// Command forgebase is the command-line tool of Forgebase: the stock binary, which has no apps and
// reads its DocTypes from --doctypes DIR. An application builds its own binary from the root package:
//
//	os.Exit(forgebase.New().App(sales.App()).Run())
//
// The commands are in internal/app; this file only hands over the process.
package main

import (
	"os"

	"github.com/tvstore/forgebase-go"
)

func main() {
	os.Exit(forgebase.New().Run())
}
