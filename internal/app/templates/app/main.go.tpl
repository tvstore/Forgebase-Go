// Command {{name}} is the binary of the {{name}} app: the Forgebase command line with this app compiled in.
package main

import (
	"os"

	"github.com/tvstore/forgebase-go"

	"{{name}}"
)

// binary is the whole wiring: the apps of this binary, in dependency order.
func binary() *forgebase.Binary {
	return forgebase.New().App({{name}}.App())
}

func main() {
	os.Exit(binary().Run())
}
