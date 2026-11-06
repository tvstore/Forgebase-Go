package db

// Hooks for the tests of package db_test.

// ExpectedChecksumForTest is the checksum of a framework migration as the Rust implementation
// recorded it.
func ExpectedChecksumForTest(version int32) string { return expectedChecksums[version] }

// WrapForTest is wrap: the conversion of a driver error.
func WrapForTest(err error) error { return wrap(err) }
