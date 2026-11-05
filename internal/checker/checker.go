// Package checker runs HTTP health checks: one goroutine per site, each on its own ticker.
package checker

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/Ramya-Shah/uptime-monitor/internal/config"
)

// Result is the outcome of a single check.
type Result struct {
	Site    string
	At      time.Time
	Status  int // 0 if no response was received
	Latency time.Duration
	OK      bool
	Err     string
}

// Check does one GET with its own timeout. 2xx and 3xx count as up.
func Check(ctx context.Context, client *http.Client, s config.Site) Result {
	ctx, cancel := context.WithTimeout(ctx, s.Timeout)
	defer cancel()

	r := Result{Site: s.Name, At: time.Now().UTC()}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.URL, nil)
	if err != nil {
		r.Err = err.Error()
		return r
	}
	req.Header.Set("User-Agent", "uptime-monitor/1.0")

	start := time.Now()
	resp, err := client.Do(req)
	r.Latency = time.Since(start)
	if err != nil {
		r.Err = err.Error()
		return r
	}
	resp.Body.Close()

	r.Status = resp.StatusCode
	r.OK = resp.StatusCode >= 200 && resp.StatusCode < 400
	if !r.OK {
		r.Err = resp.Status
	}
	return r
}

// Run starts one goroutine per site and sends every result to out.
// It blocks until ctx is cancelled and all in-flight checks have finished,
// then closes out.
func Run(ctx context.Context, client *http.Client, sites []config.Site, out chan<- Result) {
	var wg sync.WaitGroup
	for _, s := range sites {
		wg.Add(1)
		go func(s config.Site) {
			defer wg.Done()
			loop(ctx, client, s, out)
		}(s)
	}
	wg.Wait()
	close(out)
}

func loop(ctx context.Context, client *http.Client, s config.Site, out chan<- Result) {
	// A check already started should finish even if shutdown begins,
	// so it gets a context that ignores ctx's cancellation (its own timeout still applies).
	checkCtx := context.WithoutCancel(ctx)

	t := time.NewTicker(s.Interval)
	defer t.Stop()
	for {
		out <- Check(checkCtx, client, s) // first check runs immediately
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if ctx.Err() != nil { // both were ready; shutdown wins
				return
			}
		}
	}
}
