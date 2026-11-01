package http

import "testing"

func TestLogSafeStripsLineBreaks(t *testing.T) {
	if got, want := logSafe("a\r\nb\nc\rd"), "abcd"; got != want {
		t.Fatalf("logSafe = %q, want %q", got, want)
	}
}
