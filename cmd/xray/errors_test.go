package main

import (
	"errors"
	"testing"
)

func TestExitCodes(t *testing.T) {
	if got := exitCodeFor(nil); got != 0 {
		t.Errorf("exitCodeFor(nil) = %d, want 0", got)
	}
	if got := exitCodeFor(errors.New("x")); got != 1 {
		t.Errorf("exitCodeFor(plain err) = %d, want 1", got)
	}
	wrapped := silentCode(errors.New("inner"), 2)
	if got := exitCodeFor(wrapped); got != 2 {
		t.Errorf("exitCodeFor(silentCode(_, 2)) = %d, want 2", got)
	}
	if !isSilent(wrapped) {
		t.Error("isSilent(silentCode(...)) = false, want true")
	}
	if isSilent(errors.New("plain")) {
		t.Error("isSilent(plain) = true, want false")
	}
	// Errors.As chain still finds it through wrapping.
	if got := exitCodeFor(errors.Join(errors.New("a"), wrapped)); got != 2 {
		t.Errorf("exitCodeFor(joined silent) = %d, want 2", got)
	}
}
