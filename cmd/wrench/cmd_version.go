package main

import (
	"fmt"

	"github.com/ordo-systems/wrench/internal/version"
	"github.com/spf13/cobra"
)

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Args:  noArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			_, err := fmt.Fprintf(c.OutOrStdout(), "wrench %s\n", version.String())
			return err
		},
	}
}
