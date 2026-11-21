package main

import (
	"os"

	"github.com/ordo-systems/wrench/internal/app/backup"
	"github.com/spf13/cobra"
)

func newBackupCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "backup",
		Short: "Run one backup and exit",
		Args:  noArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			b, err := backup.New(os.LookupEnv)
			if err != nil {
				return err
			}

			return b.Once(c.Context())
		},
	}
}
