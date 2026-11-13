// Command deploydelta compares Prometheus metrics before and after a deploy
// and reports what shifted, with significance - so "did this deploy break
// something?" stops being a 15-dashboard ritual.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"
)

func main() {
	var (
		promURL  = flag.String("prometheus", envOr("DEPLOYDELTA_PROMETHEUS", "http://localhost:9090"), "Prometheus base URL (env DEPLOYDELTA_PROMETHEUS)")
		service  = flag.String("service", envOr("DEPLOYDELTA_SERVICE", ""), "service name as it appears in service/job/app labels (env DEPLOYDELTA_SERVICE)")
		since    = flag.String("since", "", "deploy time, RFC3339 or unix seconds (env DEPLOYDELTA_SINCE)")
		window   = flag.Duration("window", 15*time.Minute, "comparison window on each side of the deploy")
		metric   = flag.String("metric", "", "override: use this metric instead of convention discovery")
		query    = flag.String("query", "", "override: full PromQL increase expression; must return by-group vectors")
		minCount = flag.Float64("min-count", 20, "minimum post-window count for a group to be flagged (low-traffic noise floor)")
		ratioTh  = flag.Float64("ratio", 2.0, "post/pre ratio at or above which a group counts as a regression")
		pThresh  = flag.Float64("p", 0.01, "p-value below which a shift counts as significant")
		noFail   = flag.Bool("no-fail", false, "always exit 0, only report")
	)
	flag.Parse()

	deployAt, err := parseTime(*since)
	if err != nil {
		fatal("--since is required and must be RFC3339 or unix seconds: %v", err)
	}
	now := time.Now()
	preEnd := deployAt
	preStart := deployAt.Add(-*window)
	postEnd := deployAt.Add(*window)
	incomplete := false
	if postEnd.After(now) {
		postEnd = now
		incomplete = true
	}
	_ = preStart

	pc := newPromClient(strings.TrimRight(*promURL, "/"))
	wStr := promDuration(*window)

	var families []discovery
	if *metric != "" {
		families = []discovery{{Metric: *metric}}
	} else if *query == "" {
		var notes []string
		families, notes, err = discoverFamilies(pc, *service)
		if err != nil {
			fatal("discovery: %v", err)
		}
		if len(families) == 0 {
			fmt.Println("No known request-counter metric families matched.")
			for _, n := range notes {
				fmt.Println("hint:", n)
			}
			fmt.Println("Use --metric or --query to compare explicitly.")
			os.Exit(2)
		}
	}

	var allShifts []shift
	queries := 0
	if *query != "" {
		shifts, err := runComparison(pc, "custom", *query, *query, preEnd, postEnd, *minCount, *ratioTh, *pThresh)
		if err != nil {
			fatal("query: %v", err)
		}
		allShifts = append(allShifts, shifts...)
		queries++
	} else {
		for _, f := range families {
			q := f.increaseQuery(*service, wStr)
			totalQ := f.totalQuery(*service, wStr)
			shifts, err := runComparison(pc, f.Metric, q, totalQ, preEnd, postEnd, *minCount, *ratioTh, *pThresh)
			if err != nil {
				fmt.Fprintf(os.Stderr, "warning: %s: %v\n", f.Metric, err)
				continue
			}
			allShifts = append(allShifts, shifts...)
			queries++
		}
	}

	printReport(deployAt, *window, incomplete, queries, allShifts)
	if *noFail {
		return
	}
	for _, s := range allShifts {
		if s.Flagged {
			os.Exit(1)
		}
	}
}

// runComparison queries the pre and post windows for one family plus totals.
func runComparison(pc *promClient, metric, groupQuery, totalQuery string, preEnd, postEnd time.Time, minCount, ratioTh, pThresh float64) ([]shift, error) {
	pre, err := pc.queryInstant(groupQuery, preEnd)
	if err != nil {
		return nil, err
	}
	post, err := pc.queryInstant(groupQuery, postEnd)
	if err != nil {
		return nil, err
	}
	preTot, err := pc.queryInstant(totalQuery, preEnd)
	if err != nil {
		return nil, err
	}
	postTot, err := pc.queryInstant(totalQuery, postEnd)
	if err != nil {
		return nil, err
	}
	tp, err := firstValue(preTot)
	if err != nil {
		return nil, err
	}
	tq, err := firstValue(postTot)
	if err != nil {
		return nil, err
	}
	return compareResults(metric, pre, post, tp, tq, minCount, ratioTh, pThresh)
}

func firstValue(ss []sample) (float64, error) {
	if len(ss) == 0 {
		return 0, fmt.Errorf("no total result")
	}
	var sum float64
	for _, s := range ss {
		v, err := s.float()
		if err != nil {
			return 0, err
		}
		sum += v
	}
	return sum, nil
}

func printReport(deployAt time.Time, window time.Duration, incomplete bool, families int, shifts []shift) {
	fmt.Printf("deploydelta: deploy at %s, window %s on each side", deployAt.UTC().Format(time.RFC3339), window)
	if incomplete {
		fmt.Print(" (post window still open: partial data)")
	}
	fmt.Printf(" - %d metric familie(s) compared\n\n", families)
	if len(shifts) == 0 {
		fmt.Println("no traffic groups found in either window")
		return
	}
	flagged := 0
	for _, s := range shifts {
		if s.Flagged {
			flagged++
		}
	}
	fmt.Printf("%-60s %12s %12s %8s %8s  %s\n", "GROUP", "PRE", "POST", "RATIO", "P", "VERDICT")
	shown := 0
	for _, s := range shifts {
		if !s.Flagged && shown >= 20 {
			continue
		}
		verdict := "ok"
		if s.Flagged {
			verdict = "REGRESSION"
		}
		fmt.Printf("%-60s %12.1f %12.1f %8s %8.4f  %s\n",
			truncate(s.Group, 60), s.Pre, s.Post, ratioStr(s.Ratio), s.PValue, verdict)
		shown++
	}
	fmt.Printf("\n%d group(s), %d flagged\n", len(shifts), flagged)
}

func ratioStr(r float64) string {
	if r > 1e6 {
		return "new"
	}
	return fmt.Sprintf("%.2fx", r)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func parseTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, fmt.Errorf("empty")
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	var sec int64
	if _, err := fmt.Sscanf(s, "%d", &sec); err == nil && sec > 0 {
		return time.Unix(sec, 0), nil
	}
	return time.Time{}, fmt.Errorf("unparseable %q", s)
}

func promDuration(d time.Duration) string {
	m := int(d.Minutes())
	if m >= 1 {
		return fmt.Sprintf("%dm", m)
	}
	return fmt.Sprintf("%ds", int(d.Seconds()))
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "deploydelta: "+format+"\n", args...)
	os.Exit(2)
}
