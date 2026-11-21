// Command wrench-livecheck runs the pilot's end-to-end checks against a running Wrench and prints a
// report: for each model a streamed tool call, its tool result and a cancellation, then the journal's costs.
// Against live providers each model costs a few short requests.
//
//	WRENCH_TOKEN=wrk_... wrench-livecheck -url https://wrench.example.com
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/ordo-systems/wrench/tests/livecheck"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ok, err := run(ctx, os.Args[1:], os.Getenv("WRENCH_TOKEN"), os.Stdout)
	if err != nil {
		fmt.Fprintln(os.Stderr, "wrench-livecheck:", err)
		os.Exit(2)
	}
	if !ok {
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, token string, out io.Writer) (bool, error) {
	flags := flag.NewFlagSet("wrench-livecheck", flag.ContinueOnError)
	url := flags.String("url", os.Getenv("WRENCH_URL"), "Wrench URL")
	models := flags.String("models", "", "comma-separated models (default: the whole tier catalog)")
	cancelPrompt := flags.String("cancel-prompt", "", "prompt with a long answer for the cancellation check")
	settle := flags.Duration("settle", 3*time.Minute, "how long to wait for confirmed costs")
	asJSON := flags.Bool("json", false, "print the report as JSON")
	if err := flags.Parse(args); err != nil {
		return false, err
	}
	if *url == "" || token == "" {
		return false, fmt.Errorf("set -url (or WRENCH_URL) and WRENCH_TOKEN")
	}
	o := livecheck.Options{URL: strings.TrimRight(*url, "/"), Token: token, CancelPrompt: *cancelPrompt, Settle: *settle}
	if *models != "" {
		o.Models = strings.Split(*models, ",")
	}
	report, err := livecheck.Run(ctx, o)
	if err != nil {
		return false, err
	}
	if *asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return report.OK(), enc.Encode(report)
	}
	// tabwriter buffers; write errors surface from Flush.
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintf(w, "session %s\n\nMODEL\tSTEP\tRESULT\tDETAIL\n", report.Session)
	for _, r := range report.Results {
		state := "ok"
		if !r.OK {
			state = "FAIL"
		}
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", r.Model, r.Step, state, r.Detail)
	}
	_, _ = fmt.Fprint(w, "\nATTEMPT\tMODEL\tOUTCOME\tCOST\tPROVIDER\tGENERATION\n")
	for _, a := range report.Attempts {
		cost := "pending"
		if a.Cost != nil {
			cost = "$" + *a.Cost
		}
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", a.ID, a.RequestedModel, a.Outcome, cost, a.InferenceProvider, a.GenerationID)
	}
	return report.OK(), w.Flush()
}
