package main

import (
	"os"

	"github.com/ordo-systems/wrench/internal/app/backup"
	"github.com/spf13/cobra"
)

// newRestoreCmd restores a snapshot from the s3 or local repository and writes the restored policy files to
// --config-out.
func newRestoreCmd() *cobra.Command {
	var from, configOut string
	c := &cobra.Command{
		Use:   "restore [snapshot]",
		Short: "Restore a snapshot (default latest) and exit",
		Args:  maxArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if from != "s3" && from != "local" {
				return usagef("--from must be s3 or local, got %q", from)
			}

			snapshot := "latest"
			if len(args) == 1 {
				snapshot = args[0]
			}

			b, err := backup.New(os.LookupEnv)
			if err != nil {
				return err
			}

			return b.Restore(c.Context(), from, snapshot, configOut)
		},
	}
	c.Flags().StringVar(&from, "from", "s3", "repository to restore from: s3 or local")
	c.Flags().StringVar(&configOut, "config-out", "/restore/config", "directory for restored policy files")
	return c
}
