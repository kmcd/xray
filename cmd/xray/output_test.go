package main

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestParseMode(t *testing.T) {
	cases := []struct {
		in      string
		want    Mode
		wantErr bool
	}{
		{"", ModeAuto, false},
		{"auto", ModeAuto, false},
		{"quiet", ModeQuiet, false},
		{"json", ModeJSON, false},
		{"log", ModeLog, false},
		{"AUTO", ModeAuto, true},
		{"yaml", ModeAuto, true},
		{" auto", ModeAuto, true},
	}
	for _, c := range cases {
		got, err := ParseMode(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("ParseMode(%q) err = %v, wantErr = %v", c.in, err, c.wantErr)
			continue
		}
		if !c.wantErr && got != c.want {
			t.Errorf("ParseMode(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestModeString(t *testing.T) {
	cases := map[Mode]string{
		ModeAuto:  "auto",
		ModeQuiet: "quiet",
		ModeJSON:  "json",
		ModeLog:   "log",
	}
	for m, want := range cases {
		if got := m.String(); got != want {
			t.Errorf("Mode(%d).String() = %q, want %q", m, got, want)
		}
	}
}

func TestResolveMode_QuietAlias(t *testing.T) {
	got, err := ResolveMode("", true)
	if err != nil {
		t.Fatalf("ResolveMode(\"\", true) err = %v", err)
	}
	if got != ModeQuiet {
		t.Errorf("ResolveMode(\"\", true) = %v, want ModeQuiet", got)
	}
}

func TestResolveMode_QuietAndOutputQuiet(t *testing.T) {
	got, err := ResolveMode("quiet", true)
	if err != nil {
		t.Fatalf("ResolveMode(\"quiet\", true) err = %v", err)
	}
	if got != ModeQuiet {
		t.Errorf("ResolveMode(\"quiet\", true) = %v, want ModeQuiet", got)
	}
}

func TestResolveMode_QuietConflict(t *testing.T) {
	for _, out := range []string{"auto", "json", "log"} {
		_, err := ResolveMode(out, true)
		if err == nil {
			t.Errorf("ResolveMode(%q, true) err = nil, want conflict error", out)
		} else if !strings.Contains(err.Error(), "conflict") {
			t.Errorf("ResolveMode(%q, true) err = %v, want conflict-mentioning error", out, err)
		}
	}
}

func TestResolveMode_OutputOnly(t *testing.T) {
	cases := map[string]Mode{
		"":     ModeAuto,
		"auto": ModeAuto,
		"json": ModeJSON,
		"log":  ModeLog,
	}
	for in, want := range cases {
		got, err := ResolveMode(in, false)
		if err != nil {
			t.Errorf("ResolveMode(%q, false) err = %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ResolveMode(%q, false) = %v, want %v", in, got, want)
		}
	}
}

func TestResolveMode_InvalidOutput(t *testing.T) {
	_, err := ResolveMode("yaml", false)
	if err == nil {
		t.Error("ResolveMode(\"yaml\", false) err = nil, want error")
	}
}

func TestIsTTY_NilFile(t *testing.T) {
	if IsTTY(nil) {
		t.Error("IsTTY(nil) = true, want false")
	}
}

func TestIsTTY_RegularFile(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "tty")
	if err != nil {
		t.Fatalf("create temp: %v", err)
	}
	defer f.Close()
	if IsTTY(f) {
		t.Error("IsTTY(<regular file>) = true, want false")
	}
}

func TestEmitSummary_WireShape(t *testing.T) {
	// Frozen JSON shape. Any rename or field removal here must bump the
	// output_schema_version per docs/spec.md.
	var buf bytes.Buffer
	if err := EmitSummary(&buf, SummaryEvent{
		TS:       "2026-06-09T14:11:02Z",
		Kind:     "summary", // forced regardless of input
		Artifact: "/tmp/xray-export.tar.gz",
		SHA256:   "deadbeef",
		Rows:     map[string]int{"prs": 120},
		ExitCode: 0,
	}); err != nil {
		t.Fatalf("EmitSummary: %v", err)
	}
	got := buf.String()
	if !strings.HasSuffix(got, "\n") {
		t.Errorf("EmitSummary output does not end with newline: %q", got)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(got), &m); err != nil {
		t.Fatalf("EmitSummary output is not valid JSON: %v\n%s", err, got)
	}
	wantKeys := []string{"ts", "kind", "artifact", "sha256", "rows", "exit_code"}
	for _, k := range wantKeys {
		if _, ok := m[k]; !ok {
			t.Errorf("SummaryEvent missing key %q in JSON output: %s", k, got)
		}
	}
	if m["kind"] != "summary" {
		t.Errorf("kind = %v, want \"summary\"", m["kind"])
	}
}

func TestEmitSummary_KindIsForced(t *testing.T) {
	var buf bytes.Buffer
	if err := EmitSummary(&buf, SummaryEvent{Kind: "not_summary"}); err != nil {
		t.Fatalf("EmitSummary: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("EmitSummary output is not valid JSON: %v", err)
	}
	if m["kind"] != "summary" {
		t.Errorf("kind = %v, want \"summary\" (must be forced)", m["kind"])
	}
}

func TestEmitSummary_OmitsEmptyOptionals(t *testing.T) {
	var buf bytes.Buffer
	if err := EmitSummary(&buf, SummaryEvent{
		TS:       "2026-06-09T14:11:02Z",
		Artifact: "/tmp/x.tar.gz",
		ExitCode: 0,
	}); err != nil {
		t.Fatalf("EmitSummary: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "sha256") {
		t.Errorf("sha256 should be omitempty when empty: %s", out)
	}
	if strings.Contains(out, "rows") {
		t.Errorf("rows should be omitempty when nil: %s", out)
	}
}
