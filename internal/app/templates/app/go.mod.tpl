module {{name}}

{{go_directives}}

require github.com/tvstore/forgebase-go v0.0.0

// The versions Forgebase was tested with: the requirements of the Forgebase checkout. `go mod tidy`
// keeps what the app needs.
require (
{{requires}}
)

// The Forgebase checkout this app was created for. To use a released version instead, delete this line
// and run `go get github.com/tvstore/forgebase-go@<version>`.
replace github.com/tvstore/forgebase-go => {{forgebase_path}}
