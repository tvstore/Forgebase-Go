// Command {{name}} is the binary of the {{name}} app: the Forgebase command line with this app compiled in.
package main

import (
	"os"

	"github.com/tvstore/forgebase-go"

	apppkg "{{name}}"
)

// apps is the whole wiring: the apps of this binary, in dependency order.
func apps() *forgebase.Binary {
	return forgebase.New().App(apppkg.App())
}

func main() {
	os.Exit(apps().Run())
}
