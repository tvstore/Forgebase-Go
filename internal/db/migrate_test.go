package db

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"testing"
)

// Rust: migrate.rs::migrations_are_strictly_ordered
func TestMigrationsAreStrictlyOrdered(t *testing.T) {
	for i, m := range Migrations {
		if i > 0 && Migrations[i-1].Version >= m.Version {
			t.Errorf("migration %d (%s) is not above %d", m.Version, m.Name, Migrations[i-1].Version)
		}
		if strings.TrimSpace(m.SQL) == "" {
			t.Errorf("migration %d (%s) is empty", m.Version, m.Name)
		}
	}
}

// Rust: migrate.rs::bootstrap_matches_first_migration
func TestBootstrapMatchesFirstMigration(t *testing.T) {
	// The bootstrap table must stay compatible with the CREATE TABLE IF NOT EXISTS in 0001.
	if !strings.Contains(Migrations[0].SQL, "CREATE TABLE IF NOT EXISTS fb_migration") {
		t.Error("migration 1 does not create fb_migration")
	}
	if !strings.Contains(bootstrapSQL, "version integer PRIMARY KEY") {
		t.Error("the bootstrap SQL changed")
	}
}

// expectedChecksums are the SHA-256 of the framework migration files of the Rust implementation,
// which is what PostgreSQL computes for fb_migration.checksum (sha256 over the UTF-8 bytes of the
// file). A database migrated by Rust verifies in Go only while these stay the same, so a changed
// migration file fails here; the files are never edited, a new numbered one is added instead.
var expectedChecksums = map[int32]string{
	1:  "026b1eff9bdcc03ac6bae10f6ee1375151a8ea6fa9c5503cf579a33ec73435d3",
	2:  "b76f24860c8e99ce350c7fb0216b210beb2bb187651be274929f97188ac0e232",
	3:  "93d730328241373f4607f2a96537d396fbe35506487a29c952a05f86e2723b1b",
	4:  "474103338ae7097aefead5bc0b82c5d0719707ff2ff56c961b579f5442a385a1",
	5:  "40c1075c0730e30c8c5c4f111a7e09150d811649e08013f289751f33deb4f3a3",
	6:  "bbfab1b47e90ec978185da803a0d7ba0e3fad43a40fef756069baa611df5329e",
	7:  "28832e1f51ec460436202a03b56fe5d39a62a4d1c14aa118d8b2308d178439f6",
	8:  "53e8f97a0da2c42859c3fe6d72d5db09d1fb79510c37963afa2fb70e7f832b61",
	9:  "653d6ddd6e770076a6a47820268f3548138a51ded8715edb3a4a310fd657db88",
	10: "33d72fc27b1dae069601b71a33e86865fb96bba039146da737d38394052702e8",
	11: "0edc024228bf72fe112a2aae1ed8f40350a354ca81133b3a0596c1532df3006b",
	12: "28d28650605a210dceab465e2d49854c56235b1d5e5f7d01b6ef57c703274efe",
	13: "cf9095eea0377d1ba93ef35de87263bf13a55f7bf06e3b39f1e0008a1a838193",
}

// Not in Rust: the cross-implementation guard (the Rust repository is not available in CI).
func TestMigrationFilesAreByteIdenticalToRust(t *testing.T) {
	if len(Migrations) != len(expectedChecksums) {
		t.Fatalf("%d migrations, %d expected checksums", len(Migrations), len(expectedChecksums))
	}
	for _, m := range Migrations {
		sum := sha256.Sum256([]byte(m.SQL))
		if got := hex.EncodeToString(sum[:]); got != expectedChecksums[m.Version] {
			t.Errorf("migration %d (%s): SHA-256 %s, the Rust file has %s", m.Version, m.Name, got, expectedChecksums[m.Version])
		}
		if strings.Contains(m.SQL, "\r") {
			t.Errorf("migration %d (%s) has CR characters: the files are LF only (.gitattributes)", m.Version, m.Name)
		}
	}
}

// Rust: migrate.rs (the names and versions of MIGRATIONS)
func TestMigrationNamesAndVersions(t *testing.T) {
	var got []string
	for _, m := range Migrations {
		got = append(got, strconv.Itoa(int(m.Version))+":"+m.Name)
	}
	want := "1:init 2:series 3:auth 4:apps 5:jobs 6:schedules 7:finished_jobs_index 8:workflow_log " +
		"9:saved_views 10:webhooks 11:fixtures 12:custom_fields 13:user_permissions"
	if strings.Join(got, " ") != want {
		t.Errorf("got %v", got)
	}
}
