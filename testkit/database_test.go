package testkit

import (
	"regexp"
	"testing"
)

// A lease is named by the package under test, not by where the checkout
// lives, so every worktree reuses one set of databases instead of leaving
// its own behind.
func TestALeaseIsNamedByThePackageNotTheCheckoutPath(t *testing.T) {
	here, err := leaseName("app_testing", 3)
	if err != nil {
		t.Fatal(err)
	}

	t.Chdir(t.TempDir())

	elsewhere, err := leaseName("app_testing", 3)
	if err != nil {
		t.Fatal(err)
	}

	if here != elsewhere {
		t.Fatalf("leased %s here and %s from another directory", here, elsewhere)
	}

	if !regexp.MustCompile(`^app_testing_[0-9a-f]{8}_3$`).MatchString(here) {
		t.Fatalf("lease = %s, want app_testing_<package hash>_3", here)
	}
}
