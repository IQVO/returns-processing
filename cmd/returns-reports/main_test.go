package main

import (
	"strings"
	"testing"
)

// The one mandatory variable is named at boot.
func TestRunNamesTheMissingDSN(t *testing.T) {
	t.Setenv("ANALYTICS_DATABASE_URL", "")
	err := run()
	if err == nil || !strings.Contains(err.Error(), "ANALYTICS_DATABASE_URL") {
		t.Fatalf("err = %v, want it to name ANALYTICS_DATABASE_URL", err)
	}
}
