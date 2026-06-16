package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/kmcd/xray/internal/inspect"
)

func newInspectCmd() *cobra.Command {
	var flagJSON bool
	cmd := &cobra.Command{
		Use:   "inspect <artifact.tar.gz>",
		Short: "Validate an xray artifact (post-hoc integrity check)",
		Long: `inspect runs five checks against a .tar.gz artifact produced by xray run:

  (a) tar_integrity     — end-to-end gzip+tar CRC validation of every member
  (b) manifest_shape    — manifest.json parses and has required fields
  (c) sqlite_integrity  — PRAGMA integrity_check returns "ok"
  (d) row_counts        — manifest.counts matches live COUNT(*) for each table
  (e) schema_version    — _schema matches manifest; schema_version is recognised

--json emits an indented JSON Report object instead of the human output.
It is independent of --output, which controls live run-time progress events.

Exit codes:
  0  all five checks pass
  1  one or more checks failed (report still rendered)
  2  usage error (missing argument, artifact path does not exist)`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			artifactPath := args[0]

			// Exit 2 for usage-level problems: file does not exist.
			if _, err := os.Stat(artifactPath); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "xray inspect: %v\n", err)
				return silentCode(err, 2)
			}

			report, err := inspect.Inspect(cmd.Context(), artifactPath)
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "xray inspect: %v\n", err)
				return silentCode(err, 1)
			}

			if flagJSON {
				if err := renderJSONReport(cmd.OutOrStdout(), report); err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "xray inspect: encode json: %v\n", err)
					return silentCode(err, 1)
				}
			} else {
				renderHumanReport(cmd.OutOrStdout(), report)
			}

			if !report.OK {
				return silentCode(fmt.Errorf("one or more checks failed"), 1)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&flagJSON, "json", false, "emit JSON report instead of human-readable output")
	return cmd
}

// renderHumanReport writes one line per check to w, followed by a summary line.
//
// Format:
//
//	PASS  tar_integrity      512 members, 1.2 MB read
//	FAIL  row_counts         table=commits manifest=4218 db=4200
//	...
//	PASS
//	  — or —
//	FAIL (2 checks failed)
func renderHumanReport(w io.Writer, r *inspect.Report) {
	const nameWidth = 18
	failed := 0
	for _, c := range r.Checks {
		status := "PASS"
		if !c.Pass {
			status = "FAIL"
			failed++
		}
		if c.Detail != "" {
			fmt.Fprintf(w, "%-4s  %-*s  %s\n", status, nameWidth, c.Name, c.Detail)
		} else {
			fmt.Fprintf(w, "%-4s  %-*s\n", status, nameWidth, c.Name)
		}
	}
	fmt.Fprintln(w)
	if r.OK {
		fmt.Fprintln(w, "PASS")
	} else {
		fmt.Fprintf(w, "FAIL (%d check(s) failed)\n", failed)
	}
}

// renderJSONReport encodes r as indented JSON to w.
func renderJSONReport(w io.Writer, r *inspect.Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(r)
}
