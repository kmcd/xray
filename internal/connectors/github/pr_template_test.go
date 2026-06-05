package github

import (
	"math"
	"testing"
)

func TestParseTemplate(t *testing.T) {
	tpl := parseTemplate("## Summary\n\nSome blurb\n\n## Test plan\n\n## Risks\n")
	if tpl == nil {
		t.Fatal("expected template, got nil")
	}
	if len(tpl.headings) != 3 {
		t.Fatalf("expected 3 headings, got %d: %v", len(tpl.headings), tpl.headings)
	}
}

func TestParseTemplateNoHeadings(t *testing.T) {
	if tpl := parseTemplate("just prose with no headings\n"); tpl != nil {
		t.Errorf("expected nil for header-less template, got %+v", tpl)
	}
}

func TestTemplateScore(t *testing.T) {
	tpl := parseTemplate("## Summary\n## Test plan\n## Risks\n")
	body := "## Summary\n\nfoo\n\n## Test plan\n\nbar\n"
	got := tpl.score(body)
	want := 2.0 / 3.0
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("score = %v, want %v", got, want)
	}
}

func TestTemplateScoreAllPresent(t *testing.T) {
	tpl := parseTemplate("## A\n## B\n")
	if got := tpl.score("the a part\nand the b part\n"); math.Abs(got-1.0) > 1e-9 {
		t.Errorf("score = %v, want 1", got)
	}
}

func TestTemplateScoreNilTemplate(t *testing.T) {
	var tpl *template
	if got := tpl.score("anything"); got != 0 {
		t.Errorf("nil template score = %v, want 0", got)
	}
}
