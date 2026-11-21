package main

import (
	"os"

	"github.com/ordo-systems/wrench/internal/app/server"
	"github.com/spf13/cobra"
)

func newRunServerCmd() *cobra.Command {
	var configPath string
	c := &cobra.Command{
		Use:   "server",
		Short: "Serve the API, proxy and dashboard",
		Args:  noArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return server.Run(c.Context(), resolveConfig(c, configPath), os.LookupEnv, nil)
		},
	}
	configFlag(c, &configPath)
	return c
}
