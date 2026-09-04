package mirror

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
)

// AnyFailed reports whether the run needs a nonzero exit.
//
// A withheld package counts. The run's contract is that every configured repository
// holds each planned version afterwards, and an app built but kept out of the registry
// because its desk bundles would not compile does not meet it — exiting clean would
// make that a green run whose registry is quietly missing an app.
func AnyFailed(results []Result) bool {
	for _, result := range results {
		if result.Action == ActionFailed || result.Action == ActionWithheldNoAssets {
			return true
		}
	}
	return false
}

// RenderResults writes the human-readable run summary.
func RenderResults(results []Result) string {
	var b strings.Builder
	w := tabwriter.NewWriter(&b, 2, 4, 2, ' ', 0)
	fmt.Fprintln(w, "APP\tVERSION\tACTION\tSIZE\tTIME\tDETAIL")
	for _, r := range results {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			r.Slug, r.Version, r.Action, humanSize(r.SizeBytes),
			r.Duration.Round(10_000_000), firstLine(r.Detail))
	}
	w.Flush()
	return b.String()
}

// RenderPlan writes the human-readable dry-run listing.
func RenderPlan(plan *Plan) string {
	var b strings.Builder
	if len(plan.Items) == 0 {
		b.WriteString("Nothing to build — the registry is up to date.\n")
	} else {
		w := tabwriter.NewWriter(&b, 2, 4, 2, ' ', 0)
		fmt.Fprintln(w, "APP\tVERSION\tREF\tREASON")
		for _, item := range plan.Items {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", item.Slug, item.Version, item.Ref, item.Reason)
		}
		w.Flush()
	}
	for _, skip := range plan.Skipped {
		fmt.Fprintf(&b, "skip %s: %s\n", skip.Slug, skip.Detail)
	}
	return b.String()
}

// WriteReport writes the machine-readable run report.
func WriteReport(path string, results []Result) error {
	data, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// AppendStepSummary mirrors the summary into GitHub's job summary when the
// run happens inside Actions; a no-op everywhere else.
func AppendStepSummary(results []Result) {
	path := os.Getenv("GITHUB_STEP_SUMMARY")
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()

	fmt.Fprintln(f, "## fpm mirror")
	fmt.Fprintln(f, "| app | version | action | size | detail |")
	fmt.Fprintln(f, "|---|---|---|---|---|")
	for _, r := range results {
		fmt.Fprintf(f, "| %s | %s | %s | %s | %s |\n",
			r.Slug, r.Version, r.Action, humanSize(r.SizeBytes), firstLine(r.Detail))
	}
}

func humanSize(n int64) string {
	switch {
	case n <= 0:
		return "-"
	case n < 1<<20:
		return fmt.Sprintf("%dK", n>>10)
	default:
		return fmt.Sprintf("%.1fM", float64(n)/(1<<20))
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
