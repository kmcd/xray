package main

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/kmcd/xray/internal/config"
)

func newValidateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "validate <config>",
		Short: "Offline syntactic and schema check on a config file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := args[0]
			cfg, meta, err := config.Load(path)
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "%s: %v\n", path, err)
				return silentCode(errors.New("config load failed"), 2)
			}
			diags := config.Validate(cfg, meta, path)
			if len(diags) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "ok  config valid")
				return nil
			}
			for _, d := range diags {
				fmt.Fprintln(cmd.ErrOrStderr(), d.Error())
			}
			return silentCode(errors.New("validation failed"), 2)
		},
	}
}
