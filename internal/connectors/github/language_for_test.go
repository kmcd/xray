package github

import "testing"

func TestLanguageFor(t *testing.T) {
	if got := languageFor("foo.go", nil, false); got == "" {
		t.Errorf("expected non-empty language for foo.go, got empty")
	}
	if got := languageFor("foo.bin", []byte{0, 1, 2, 3}, true); got != "" {
		t.Errorf("expected empty language for binary, got %q", got)
	}
	// Large non-binary content with unknown extension -> falls through to empty.
	big := make([]byte, 1024*1024+1)
	if got := languageFor("foo.unknown", big, false); got != "" {
		t.Errorf("expected empty language for oversized content, got %q", got)
	}
}

func TestSetCaptureHarnessContent(t *testing.T) {
	c := &Connector{}
	c.SetCaptureHarnessContent(true)
	if !c.capture {
		t.Errorf("expected capture=true after Set, got false")
	}
}
