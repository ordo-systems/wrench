package main

import (
	"github.com/ordo-systems/wrench/internal/app/mocks"
	"github.com/spf13/cobra"
)

func newMockLLMCmd() *cobra.Command {
	var listen string
	c := &cobra.Command{
		Use:   "llm",
		Short: "Run the OpenRouter-compatible LLM mock",
		Args:  noArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return mocks.RunLLM(c.Context(), listen)
		},
	}
	c.Flags().StringVar(&listen, "listen", ":8000", "listen address")
	return c
}
