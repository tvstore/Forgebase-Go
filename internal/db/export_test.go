package db

import (
	"context"
	"time"
)

// Hooks for the tests of package db_test.

// ExpectedChecksumForTest is the checksum of a framework migration as the Rust implementation
// recorded it.
func ExpectedChecksumForTest(version int32) string { return expectedChecksums[version] }

// WrapForTest is wrap: the conversion of a driver error.
func WrapForTest(err error) error { return wrap(err) }

// LockTimeoutSettingForTest is lockTimeoutSetting.
func LockTimeoutSettingForTest(timeout time.Duration) string { return lockTimeoutSetting(timeout) }

// AppMigrationTablesExistForTest is appMigrationTablesExist.
func AppMigrationTablesExistForTest(ctx context.Context, q Queryer) (bool, error) {
	return appMigrationTablesExist(ctx, q)
}
