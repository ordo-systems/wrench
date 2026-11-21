package main

import (
	"github.com/ordo-systems/wrench/internal/app/server"
	"github.com/spf13/cobra"
)

func newHealthcheckCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "healthcheck <url>",
		Short: "Probe a URL and exit (container health checks)",
		Args:  exactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return server.Probe(c.Context(), args[0])
		},
	}
}
