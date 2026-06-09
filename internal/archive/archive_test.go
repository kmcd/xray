package archive

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestWriteTarGz_Deterministic guards the #93 fix: two WriteTarGz calls
// over the same input files (with identical bytes) must produce
// byte-identical .tar.gz output. The fix is the archiveEpoch ModTime on
// the gzip writer and the tar header; if either reverts to time.Now()
// this test fails immediately.
func TestWriteTarGz_Deterministic(t *testing.T) {
	dir := t.TempDir()

	srcA := filepath.Join(dir, "a.txt")
	srcB := filepath.Join(dir, "b.txt")
	if err := os.WriteFile(srcA, []byte("payload-a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(srcB, []byte("payload-b\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	files := map[string]string{srcA: "a.txt", srcB: "b.txt"}

	out1 := filepath.Join(dir, "out1.tar.gz")
	out2 := filepath.Join(dir, "out2.tar.gz")

	r1, err := WriteTarGz(out1, files)
	if err != nil {
		t.Fatalf("first WriteTarGz: %v", err)
	}
	r2, err := WriteTarGz(out2, files)
	if err != nil {
		t.Fatalf("second WriteTarGz: %v", err)
	}

	if r1.SHA256 != r2.SHA256 {
		t.Errorf("SHA256 not deterministic: %q vs %q", r1.SHA256, r2.SHA256)
	}
	if r1.Size != r2.Size {
		t.Errorf("Size not deterministic: %d vs %d", r1.Size, r2.Size)
	}

	b1, err := os.ReadFile(out1)
	if err != nil {
		t.Fatal(err)
	}
	b2, err := os.ReadFile(out2)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(b1, b2) {
		t.Errorf("archive bytes not identical: %d vs %d bytes; tar/gzip ModTime probably reverted to time.Now()", len(b1), len(b2))
	}
}
