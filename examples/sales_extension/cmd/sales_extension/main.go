// Command sales_extension is the composed binary of the sales_extension example: the framework, then sales,
// then this app.
package main

import (
	"os"

	"github.com/tvstore/forgebase-go"
	"github.com/tvstore/forgebase-go/examples/sales"
	"github.com/tvstore/forgebase-go/examples/sales_extension"
)

// The composed binary: sales, then this app (a dependency is added before the apps that depend on it).
func main() {
	os.Exit(forgebase.New().
		App(sales.App()).
		App(sales_extension.App()).
		Run())
}
