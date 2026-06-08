package github

import (
	"math"
	"testing"
)

func TestScanIndentLevels_Basic(t *testing.T) {
	// 5 lines: one level-0 (skipped), one level-1, one level-2, one level-3,
	// one blank. Tab-indented line counts as level 1.
	src := []byte("at-margin\n    one\n        two\n\t\t\tthree\n\n")
	got := scanIndentLevels(src)
	if got.n != 3 {
		t.Errorf("n = %d, want 3", got.n)
	}
	if got.total != 6 {
		t.Errorf("indent_total = %d, want 6", got.total)
	}
	if got.maxLevel != 3 {
		t.Errorf("indent_max = %d, want 3", got.maxLevel)
	}
	wantMean := 2.0
	if math.Abs(got.mean-wantMean) > 1e-9 {
		t.Errorf("indent_mean = %v, want %v", got.mean, wantMean)
	}
	// sample stddev of {1, 2, 3} = sqrt(((1-2)^2+(2-2)^2+(3-2)^2)/2) = 1.0
	if math.Abs(got.sd-1.0) > 1e-9 {
		t.Errorf("indent_sd = %v, want 1.0", got.sd)
	}
}

func TestScanIndentLevels_Empty(t *testing.T) {
	got := scanIndentLevels(nil)
	if got.n != 0 || got.total != 0 || got.maxLevel != 0 || got.mean != 0 || got.sd != 0 {
		t.Errorf("zero-content nonzero stats: %+v", got)
	}
}

func TestScanIndentLevels_SingleLine_SDZero(t *testing.T) {
	// One level-1 line; sample stddev requires n >= 2, so SD stays 0.
	got := scanIndentLevels([]byte("    one\n"))
	if got.n != 1 || got.total != 1 || got.sd != 0 {
		t.Errorf("single-line stats: %+v", got)
	}
}

func TestScanIndentLevels_TabSpaceMix(t *testing.T) {
	// "\t    " = tab(4) + 4 spaces = 8 raw spaces / 4 = level 2.
	got := scanIndentLevels([]byte("\t    mix\n"))
	if got.maxLevel != 2 {
		t.Errorf("maxLevel = %d, want 2 for tab+4-spaces", got.maxLevel)
	}
}

func TestComplexityHistoryExcluded(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"src/foo.go", false},
		{"src/foo_test.go", false}, // tests are NOT excluded
		{"vendor/golang.org/x/sync/sync.go", true},
		{"node_modules/react/index.js", true},
		{"__pycache__/foo.cpython-311.pyc", true},
		{"build/output.o", true},
		{"dist/bundle.js", true},
		{".venv/lib/python3.13/site/foo.py", true},
		{"package-lock.json", false}, // *.lock matches, not *.json
		{"yarn.lock", true},
		{"foo.pb.go", true},
		{"foo_pb2.py", true},
		{"foo.min.js", true},
		{"icon.png", true},
		{"binary.exe", true},
		{"docs/diagram.svg", true},
		{"foo.generated.go", true},
		{"", true},
	}
	for _, tc := range cases {
		got := complexityHistoryExcluded(tc.path)
		if got != tc.want {
			t.Errorf("complexityHistoryExcluded(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}
