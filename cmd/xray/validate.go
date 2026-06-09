package main

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/kmcd/xray/internal/config"
)

// validateResult is the JSON shape emitted by `xray validate --output json`.
type validateResult struct {
	Kind       string `json:"kind"`
	OK         bool   `json:"ok"`
	ConfigPath string `json:"config_path"`
}

func newValidateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "validate [config]",
		Short: "Offline syntactic and schema check on a config file",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := resolveConfigPath(cmd, args)
			if err != nil {
				return err
			}
			mode, err := ResolveMode(flagOutput, flagQuiet)
			if err != nil {
				return silentCode(err, 1)
			}
			cfg, meta, err := config.Load(path)
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "%s: %v\n", path, err)
				return silentCode(errors.New("config load failed"), 1)
			}
			diags := config.Validate(cfg, meta, path)
			if len(diags) == 0 {
				switch mode {
				case ModeQuiet:
					// success: nothing on stdout.
				case ModeJSON:
					_ = emitJSONLine(cmd.OutOrStdout(), validateResult{
						Kind: "validate_summary", OK: true, ConfigPath: path,
					})
				default:
					fmt.Fprintln(cmd.OutOrStdout(), "ok  config valid")
				}
				return nil
			}
			for _, d := range diags {
				fmt.Fprintln(cmd.ErrOrStderr(), d.Error())
			}
			return silentCode(errors.New("validation failed"), 1)
		},
	}
}
