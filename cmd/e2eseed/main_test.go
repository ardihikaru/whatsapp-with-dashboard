package main

import (
	"errors"
	"strings"
	"testing"
)

// The guard is the only thing standing between this seeder and the SHARED
// production MySQL. If it ever stops rejecting a MySQL DSN, a test run writes
// junk into live tables belonging to other products.
func TestSeederRefusesEveryNonSQLiteDSN(t *testing.T) {
	for _, dsn := range []string{
		"",
		"user:pass@tcp(10.10.0.2:5306)/wa_dashboard?parseTime=true",
		"root:hunter2@tcp(127.0.0.1:3306)/wa_dashboard",
		"mysql://user:pass@host/db",
		"postgres://user:pass@host/db",
		"sqlite-ish:/tmp/x.db", // prefix must match exactly
		" sqlite:/tmp/x.db",    // no leading-space tolerance
		"SQLITE:/tmp/x.db",     // case-sensitive on purpose
	} {
		t.Run(dsn, func(t *testing.T) {
			err := checkDSN(dsn)
			if err == nil {
				t.Fatalf("checkDSN(%q) allowed a non-SQLite DSN", dsn)
			}
			if dsn != "" && !errors.Is(err, ErrNotSQLite) {
				t.Errorf("checkDSN(%q) = %v, want ErrNotSQLite", dsn, err)
			}
		})
	}
}

// A rejected MySQL DSN must not carry its password into a log line.
func TestRejectionDoesNotEchoTheDSNPassword(t *testing.T) {
	err := checkDSN("wa_user:s3cr3t-p4ssw0rd@tcp(10.10.0.2:5306)/wa_dashboard")
	if err == nil {
		t.Fatal("expected a rejection")
	}
	if strings.Contains(err.Error(), "s3cr3t-p4ssw0rd") {
		t.Errorf("the rejection leaked the password: %v", err)
	}
	if !strings.Contains(err.Error(), "10.10.0.2:5306") {
		t.Errorf("the rejection should still name the host it refused: %v", err)
	}
}

func TestSeederAcceptsASQLitePath(t *testing.T) {
	if err := checkDSN("sqlite:/tmp/e2e/app.db"); err != nil {
		t.Errorf("checkDSN rejected a valid SQLite DSN: %v", err)
	}
}

// "sqlite:" with nothing after it would open a file literally named "", which
// fails later and further from the cause.
func TestSeederRejectsAPrefixWithNoPath(t *testing.T) {
	if err := checkDSN("sqlite:"); err == nil {
		t.Fatal("checkDSN(\"sqlite:\") allowed an empty path")
	}
}
