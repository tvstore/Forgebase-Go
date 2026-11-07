module github.com/tvstore/forgebase-go/examples/sales_extension

go 1.26.0

toolchain go1.27.3

require (
	github.com/tvstore/forgebase-go v0.0.0
	github.com/tvstore/forgebase-go/examples/sales v0.0.0
)

require (
	github.com/BurntSushi/toml v1.6.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/pgx/v5 v5.12.0 // indirect
	github.com/jackc/puddle/v2 v2.2.3 // indirect
	golang.org/x/crypto v0.58.0 // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/text v0.43.0 // indirect
)

replace github.com/tvstore/forgebase-go => ../..

replace github.com/tvstore/forgebase-go/examples/sales => ../sales
