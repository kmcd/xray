package github

import (
	"testing"
)

func TestIsTestPath(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"foo_test.go", true},
		{"pkg/foo_test.go", true},
		{"src/foo.test.ts", true},
		{"src/foo.spec.rb", true},
		{"spec/models/user_spec.rb", true},
		{"src/__tests__/foo.js", true},
		{"__tests__/foo.js", true},
		{"foo.go", false},
		{"testdata/foo.go", false},
		{"specifications/foo.md", false},
		{"deeply/nested/spec/foo.rb", true},
	}
	for _, c := range cases {
		got := isTestPath(c.path)
		if got != c.want {
			t.Errorf("isTestPath(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

func TestIsDependencyManifest(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"Gemfile", true},
		{"app/Gemfile", true},
		{"Gemfile.lock", true},
		{"package.json", true},
		{"sub/dir/package.json", true},
		{"go.mod", true},
		{"go.sum", true},
		{"Cargo.toml", true},
		{"requirements.txt", true},
		{"pyproject.toml", true},
		{"composer.lock", true},
		{"pom.xml", true},
		{"build.gradle", true},
		{"build.gradle.kts", true},
		{"Podfile.lock", true},
		{"mix.exs", true},
		// Negatives
		{"GEMFILE", false}, // case-sensitive
		{"my_package.json.bak", false},
		{"README.md", false},
		{"src/main.go", false},
	}
	for _, c := range cases {
		got := isDependencyManifest(c.path)
		if got != c.want {
			t.Errorf("isDependencyManifest(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

func TestScanLines_LOC(t *testing.T) {
	cases := []struct {
		name                          string
		content                       string
		total, code, blank            int
	}{
		{"empty", "", 0, 0, 0},
		{"one line no newline", "hello", 1, 1, 0},
		{"one line with newline", "hello\n", 1, 1, 0},
		{"two lines", "a\nb\n", 2, 2, 0},
		{"blank line", "a\n\nb\n", 3, 2, 1},
		{"whitespace blank", "a\n  \t\nb\n", 3, 2, 1},
		{"crlf", "a\r\nb\r\n", 2, 2, 0},
		{"trailing blank", "a\n\n", 2, 1, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := scanLines([]byte(c.content))
			if s.total != c.total || s.code != c.code || s.blank != c.blank {
				t.Errorf("scanLines(%q) total=%d code=%d blank=%d, want %d/%d/%d",
					c.content, s.total, s.code, s.blank, c.total, c.code, c.blank)
			}
		})
	}
}

func TestScanLines_Indent(t *testing.T) {
	// Lines: "no_indent", "  two", "    four", "\t\ttab8", blank, "x"
	content := "no_indent\n  two\n    four\n\t\ttab8\n\n  x\n"
	s := scanLines([]byte(content))
	if s.maxIndent != 8 {
		t.Errorf("maxIndent = %d, want 8", s.maxIndent)
	}
	// Mean over 5 non-blank lines: (0 + 2 + 4 + 8 + 2) / 5 = 3.2
	if s.meanIndent < 3.19 || s.meanIndent > 3.21 {
		t.Errorf("meanIndent = %v, want ~3.2", s.meanIndent)
	}
	if s.code != 5 || s.blank != 1 {
		t.Errorf("code/blank = %d/%d, want 5/1", s.code, s.blank)
	}
}

func TestScanLines_LineLength(t *testing.T) {
	// 20 lines of length 1..20. P95 of 1..20 (nearest-rank) = 19th index = 19.
	var b []byte
	for i := 1; i <= 20; i++ {
		for j := 0; j < i; j++ {
			b = append(b, 'x')
		}
		b = append(b, '\n')
	}
	s := scanLines(b)
	if s.maxLineLen != 20 {
		t.Errorf("maxLineLen = %d, want 20", s.maxLineLen)
	}
	// nearest-rank: ceil(0.95 * 20) = 19, sorted[18] = 19
	if s.p95LineLen != 19 {
		t.Errorf("p95LineLen = %d, want 19", s.p95LineLen)
	}
}

func TestPercentile(t *testing.T) {
	cases := []struct {
		vals []int
		p    int
		want int
	}{
		{[]int{}, 95, 0},
		{[]int{5}, 95, 5},
		{[]int{1, 2, 3, 4}, 50, 2},
		{[]int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}, 95, 10},
		{[]int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}, 90, 9},
	}
	for _, c := range cases {
		got := percentile(c.vals, c.p)
		if got != c.want {
			t.Errorf("percentile(%v, %d) = %d, want %d", c.vals, c.p, got, c.want)
		}
	}
}

func TestLeadingIndent(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"foo", 0},
		{"  foo", 2},
		{"\tfoo", 4},
		{"\t\tfoo", 8},
		{" \t foo", 1 + 4 + 1},
		{"   ", 3}, // all whitespace; counts as indent
	}
	for _, c := range cases {
		if got := leadingIndent([]byte(c.in)); got != c.want {
			t.Errorf("leadingIndent(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}
