// Command sales is the binary of the sales example app: the Forgebase command line with the sales app
// compiled in.
package main

import (
	"os"

	"github.com/tvstore/forgebase-go"
	"github.com/tvstore/forgebase-go/examples/sales"
)

// The whole wiring: the apps of this binary, in dependency order.
func main() {
	os.Exit(forgebase.New().App(sales.App()).Run())
}
