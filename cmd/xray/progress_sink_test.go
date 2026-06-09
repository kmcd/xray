package main

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/spf13/cobra"

	"github.com/kmcd/xray/internal/config"
	"github.com/kmcd/xray/internal/progress"
)

func testCmdAndCtx() (*cobra.Command, context.Context, *bytes.Buffer) {
	var buf bytes.Buffer
	c := &cobra.Command{}
	c.SetOut(&buf)
	c.SetErr(&buf)
	return c, context.Background(), &buf
}

func TestBuildProgressSink_Quiet(t *testing.T) {
	cmd, ctx, _ := testCmdAndCtx()
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	sink, stop := buildProgressSink(ctx, cmd, ModeQuiet, logger, &config.Config{}, nil, 4)
	defer stop()
	if _, ok := sink.(progress.NopSink); !ok {
		t.Errorf("quiet should yield NopSink, got %T", sink)
	}
}

func TestBuildProgressSink_JSON(t *testing.T) {
	cmd, ctx, buf := testCmdAndCtx()
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	sink, stop := buildProgressSink(ctx, cmd, ModeJSON, logger, &config.Config{}, nil, 4)
	defer stop()
	if _, ok := sink.(*progress.JSONSink); !ok {
		t.Fatalf("json should yield *JSONSink, got %T", sink)
	}
	sink.Emit(progress.Event{Kind: progress.PhaseStart, Repo: "r", Connector: "c", Phase: "p"})
	if buf.Len() == 0 {
		t.Errorf("expected JSON output to writer")
	}
}

func TestBuildProgressSink_Log(t *testing.T) {
	cmd, ctx, _ := testCmdAndCtx()
	var lbuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&lbuf, nil))
	sink, stop := buildProgressSink(ctx, cmd, ModeLog, logger, &config.Config{}, nil, 4)
	defer stop()
	if _, ok := sink.(*progress.LogSink); !ok {
		t.Fatalf("log should yield *LogSink, got %T", sink)
	}
	sink.Emit(progress.Event{Kind: progress.PhaseStart, Repo: "r", Connector: "c", Phase: "p"})
	if lbuf.Len() == 0 {
		t.Errorf("expected log output")
	}
}

func TestBuildProgressSink_AutoNonTTYFallsBackToLog(t *testing.T) {
	// cobra.Command's default writer for tests is a *bytes.Buffer, which
	// is not an *os.File — the auto path must fall back to LogSink.
	cmd, ctx, _ := testCmdAndCtx()
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	sink, stop := buildProgressSink(ctx, cmd, ModeAuto, logger, &config.Config{}, nil, 4)
	defer stop()
	if _, ok := sink.(*progress.LogSink); !ok {
		t.Errorf("auto non-TTY should yield *LogSink, got %T", sink)
	}
}
