// Package verify is a post-deploy release check: poll a URL N times and
// decide whether the release is healthy (no failures, p95 under a limit).
package verify

import (
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"slices"
	"time"

	"github.com/Ramya-Shah/uptime-monitor/internal/checker"
	"github.com/Ramya-Shah/uptime-monitor/internal/config"
)

type Options struct {
	URL      string
	Checks   int
	Interval time.Duration
	Timeout  time.Duration // per check
	MaxP95   time.Duration
}

type Report struct {
	Opts     Options
	Results  []checker.Result
	Failures int
	P50, P95 time.Duration // over successful checks, same as the monitor's SQL
	Problems []string      // why it failed; empty means pass
}

func (r Report) Pass() bool { return len(r.Problems) == 0 }

// Run performs the checks, spaced Interval apart (the first runs at once).
// If ctx is cancelled (CI job cancelled, Ctrl-C) it stops and the report fails.
func Run(ctx context.Context, client *http.Client, o Options) Report {
	site := config.Site{Name: "verify", URL: o.URL, Timeout: o.Timeout}
	rep := Report{Opts: o}

	for i := 0; i < o.Checks; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				rep.Problems = append(rep.Problems, fmt.Sprintf("interrupted after %d of %d checks", i, o.Checks))
				return rep
			case <-time.After(o.Interval):
			}
		}
		res := checker.Check(ctx, client, site)
		rep.Results = append(rep.Results, res)
		if !res.OK {
			rep.Failures++
		}
	}

	var ok []time.Duration
	for _, res := range rep.Results {
		if res.OK {
			ok = append(ok, res.Latency)
		}
	}
	rep.P50, rep.P95 = Percentile(ok, 0.50), Percentile(ok, 0.95)

	if rep.Failures > 0 {
		rep.Problems = append(rep.Problems, fmt.Sprintf("%d of %d checks failed", rep.Failures, len(rep.Results)))
	}
	if len(ok) > 0 && rep.P95 > o.MaxP95 {
		rep.Problems = append(rep.Problems, fmt.Sprintf("p95 %v is over the %v limit", ms(rep.P95), o.MaxP95))
	}
	return rep
}

// Percentile uses linear interpolation between the closest ranks, which is
// what Postgres percentile_cont does, so CLI and API numbers agree.
func Percentile(ds []time.Duration, p float64) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	s := slices.Clone(ds)
	slices.Sort(s)
	pos := p * float64(len(s)-1)
	lo, hi := int(math.Floor(pos)), int(math.Ceil(pos))
	frac := pos - float64(lo)
	return s[lo] + time.Duration(math.Round(frac*float64(s[hi]-s[lo])))
}

// Print writes a short human-readable report.
func (r Report) Print(w io.Writer) {
	o := r.Opts
	fmt.Fprintf(w, "verify %s  checks=%d interval=%v max-p95=%v\n", o.URL, o.Checks, o.Interval, o.MaxP95)
	for i, res := range r.Results {
		line := fmt.Sprintf("  #%-2d %3d  %8v  ok", i+1, res.Status, ms(res.Latency))
		if !res.OK {
			line = fmt.Sprintf("  #%-2d %3d  %8v  FAIL %s", i+1, res.Status, ms(res.Latency), res.Err)
		}
		fmt.Fprintln(w, line)
	}
	p50, p95 := "n/a", "n/a" // no successful checks means no latency to report
	if r.Failures < len(r.Results) {
		p50, p95 = ms(r.P50).String(), ms(r.P95).String()
	}
	fmt.Fprintf(w, "checks=%d failures=%d p50=%s p95=%s\n", len(r.Results), r.Failures, p50, p95)
	if r.Pass() {
		fmt.Fprintln(w, "PASS")
		return
	}
	for _, p := range r.Problems {
		fmt.Fprintln(w, "FAIL:", p)
	}
}

func ms(d time.Duration) time.Duration { return d.Round(time.Millisecond) }
