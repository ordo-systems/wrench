package main

import (
	"os"

	"github.com/ordo-systems/wrench/internal/app/backup"
	"github.com/spf13/cobra"
)

func newRunBackupCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "backup",
		Short: "Run scheduled backups until stopped",
		Args:  noArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			b, err := backup.New(os.LookupEnv)
			if err != nil {
				return err
			}
			return b.Loop(c.Context())
		},
	}
}
