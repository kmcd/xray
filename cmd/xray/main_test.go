package main

import (
	"bytes"
	"context"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
)

func TestWatchSignals_FirstCancels(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigs := make(chan os.Signal, 1)
	var buf bytes.Buffer
	exit := func(int) { t.Fatalf("exit called on first signal") }

	done := make(chan struct{})
	go func() {
		watchSignals(sigs, cancel, func() string { return "" }, &buf, exit)
		close(done)
	}()

	sigs <- syscall.SIGINT
	<-ctx.Done()
	if err := ctx.Err(); err != context.Canceled {
		t.Errorf("ctx.Err() = %v, want context.Canceled", err)
	}
	if !strings.Contains(buf.String(), "interrupt received") {
		t.Errorf("stderr missing interrupt notice:\n%s", buf.String())
	}

	close(sigs)
	<-done
}

func TestWatchSignals_SecondForceExits(t *testing.T) {
	_, cancel := context.WithCancel(context.Background())

	sigs := make(chan os.Signal, 2)
	var buf bytes.Buffer

	var (
		exitMu   sync.Mutex
		exitCode int
		exited   bool
	)
	exit := func(code int) {
		exitMu.Lock()
		exited = true
		exitCode = code
		exitMu.Unlock()
	}

	done := make(chan struct{})
	go func() {
		watchSignals(sigs, cancel, func() string { return "/tmp/xray-test-abc" }, &buf, exit)
		close(done)
	}()

	sigs <- syscall.SIGINT
	sigs <- syscall.SIGINT
	close(sigs)
	<-done

	exitMu.Lock()
	defer exitMu.Unlock()
	if !exited {
		t.Fatal("exit not called on second signal")
	}
	if exitCode != 130 {
		t.Errorf("exit code = %d, want 130", exitCode)
	}
	if !strings.Contains(buf.String(), "/tmp/xray-test-abc") {
		t.Errorf("force-exit message missing tmpdir path:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "force exit") {
		t.Errorf("force-exit message missing 'force exit':\n%s", buf.String())
	}
}

func TestWatchSignals_SecondNoTempDir(t *testing.T) {
	_, cancel := context.WithCancel(context.Background())

	sigs := make(chan os.Signal, 2)
	var buf bytes.Buffer
	var exitCode int
	exit := func(code int) { exitCode = code }

	done := make(chan struct{})
	go func() {
		watchSignals(sigs, cancel, func() string { return "" }, &buf, exit)
		close(done)
	}()

	sigs <- syscall.SIGINT
	sigs <- syscall.SIGINT
	close(sigs)
	<-done

	if exitCode != 130 {
		t.Errorf("exit code = %d, want 130", exitCode)
	}
	if !strings.Contains(buf.String(), "force exit") {
		t.Errorf("force-exit (no tmpdir) message missing 'force exit':\n%s", buf.String())
	}
	if strings.Contains(buf.String(), "temp dir") {
		t.Errorf("force-exit (no tmpdir) should not claim a leak: %q", buf.String())
	}
}

func TestTmpDirSnapshot_RoundTrip(t *testing.T) {
	tmpDirRef.Store(nil)
	if got := tmpDirSnapshot(); got != "" {
		t.Errorf("initial snapshot = %q, want empty", got)
	}
	p := "/tmp/xray-snapshot-test"
	tmpDirRef.Store(&p)
	if got := tmpDirSnapshot(); got != p {
		t.Errorf("snapshot = %q, want %q", got, p)
	}
	tmpDirRef.Store(nil)
	if got := tmpDirSnapshot(); got != "" {
		t.Errorf("cleared snapshot = %q, want empty", got)
	}
}
