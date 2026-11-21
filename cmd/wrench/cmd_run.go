package main

import (
	"github.com/spf13/cobra"
)

func newRunCmd() *cobra.Command {
	run := newGroupCmd("run", "Run a long-lived process")
	run.AddCommand(newRunServerCmd(), newRunBackupCmd())
	return run
}
