package store

import (
	"context"
	"fmt"
	"math"
	"os"
	"testing"
	"time"

	"github.com/Ramya-Shah/uptime-monitor/internal/checker"
)

// openTest connects to TEST_DATABASE_URL or skips the test.
// Tests use unique site names, so they never see each other's rows.
func openTest(t *testing.T) *Store {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; run `docker compose up -d db`")
	}
	s, err := Open(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func TestStats(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	site := fmt.Sprintf("test-%d", time.Now().UnixNano())
	now := time.Now()

	// 10 successful checks: 100, 200, ... 1000 ms.
	for i := 1; i <= 10; i++ {
		r := checker.Result{Site: site, At: now.Add(-time.Duration(i) * time.Minute), Status: 200,
			Latency: time.Duration(i*100) * time.Millisecond, OK: true}
		if err := s.Insert(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	// 1 failure inside the window (its 10 s latency must not affect percentiles)
	// and 1 old failure outside a 24h window.
	for _, at := range []time.Time{now.Add(-time.Hour), now.Add(-48 * time.Hour)} {
		r := checker.Result{Site: site, At: at, Latency: 10 * time.Second, Err: "timeout"}
		if err := s.Insert(ctx, r); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		window           time.Duration
		checks, failures int
		uptime, p50, p95 float64
	}{
		// percentile_cont interpolates: p50 of 100..1000 = 550, p95 = 900 + 0.55*100 = 955.
		{24 * time.Hour, 11, 1, 100.0 * 10 / 11, 550, 955},
		{7 * 24 * time.Hour, 12, 2, 100.0 * 10 / 12, 550, 955},
	}
	for _, tc := range tests {
		t.Run(tc.window.String(), func(t *testing.T) {
			st, err := s.Stats(ctx, site, tc.window)
			if err != nil {
				t.Fatal(err)
			}
			if st.Checks != tc.checks || st.Failures != tc.failures {
				t.Errorf("checks=%d failures=%d, want %d/%d", st.Checks, st.Failures, tc.checks, tc.failures)
			}
			for name, pair := range map[string][2]float64{
				"uptime": {*st.UptimePct, tc.uptime}, "p50": {*st.P50Ms, tc.p50}, "p95": {*st.P95Ms, tc.p95},
			} {
				if math.Abs(pair[0]-pair[1]) > 0.001 {
					t.Errorf("%s = %v, want %v", name, pair[0], pair[1])
				}
			}
		})
	}
}

func TestStatsEmpty(t *testing.T) {
	s := openTest(t)
	st, err := s.Stats(context.Background(), "no-such-site", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if st.Checks != 0 || st.UptimePct != nil || st.P95Ms != nil {
		t.Errorf("want zero checks and null stats, got %+v", st)
	}
}

func TestLatest(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	site := fmt.Sprintf("latest-%d", time.Now().UnixNano())
	now := time.Now().UTC().Truncate(time.Millisecond)
	s.Insert(ctx, checker.Result{Site: site, At: now.Add(-time.Minute), Status: 200, OK: true})
	s.Insert(ctx, checker.Result{Site: site, At: now, Status: 503, Err: "503 Service Unavailable"})

	got, err := s.Latest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if r := got[site]; r.Status != 503 || r.OK || !r.At.Equal(now) {
		t.Errorf("latest = %+v, want the 503 at %v", r, now)
	}

	// A newer success must not hide the last failure.
	s.Insert(ctx, checker.Result{Site: site, At: now.Add(time.Minute), Status: 200, OK: true})
	fails, err := s.LastFailures(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if r := fails[site]; r.Status != 503 || !r.At.Equal(now) {
		t.Errorf("last failure = %+v, want the 503 at %v", r, now)
	}
}
