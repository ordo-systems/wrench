package main

import (
	"os"

	"github.com/ordo-systems/wrench/internal/app/mocks"
	"github.com/spf13/cobra"
)

func newMockSeedCmd() *cobra.Command {
	var configPath string
	c := &cobra.Command{
		Use:   "seed",
		Short: "Fill an empty demo database with 30 days of history (WRENCH_POSTGRES_DSN)",
		Args:  noArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return mocks.RunSeed(c.Context(), resolveConfig(c, configPath), os.LookupEnv)
		},
	}
	configFlag(c, &configPath)
	return c
}
