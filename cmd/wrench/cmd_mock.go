package main

import (
	"github.com/spf13/cobra"
)

func newMockCmd() *cobra.Command {
	mock := newGroupCmd("mock", "Run a development mock")
	mock.AddCommand(newMockLLMCmd(), newMockGitLabCmd(), newMockSeedCmd())
	return mock
}
