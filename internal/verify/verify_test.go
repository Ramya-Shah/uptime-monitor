package verify

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRun(t *testing.T) {
	tests := []struct {
		name     string
		handler  func(n int64) (delay time.Duration, status int) // n = request number, from 1
		maxP95   time.Duration
		wantPass bool
		wantMsg  string // substring of the printed report
	}{
		{"healthy", func(int64) (time.Duration, int) { return 0, 200 }, time.Second, true, "PASS"},
		{"one 500 fails the release", func(n int64) (time.Duration, int) {
			if n == 3 {
				return 0, 500
			}
			return 0, 200
		}, time.Second, false, "1 of 5 checks failed"},
		{"slow p95 fails the release", func(int64) (time.Duration, int) { return 60 * time.Millisecond, 200 },
			20 * time.Millisecond, false, "over the 20ms limit"},
		{"timeout fails the release", func(n int64) (time.Duration, int) {
			if n == 2 {
				return time.Second, 200 // longer than the 200ms timeout below
			}
			return 0, 200
		}, time.Second, false, "deadline exceeded"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var count atomic.Int64 // handlers run on other goroutines
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				delay, status := tc.handler(count.Add(1))
				select {
				case <-time.After(delay):
				case <-r.Context().Done():
					return
				}
				w.WriteHeader(status)
			}))
			defer srv.Close()

			o := Options{URL: srv.URL, Checks: 5, Interval: 5 * time.Millisecond, Timeout: 200 * time.Millisecond, MaxP95: tc.maxP95}
			rep := Run(context.Background(), srv.Client(), o)

			var out bytes.Buffer
			rep.Print(&out)
			if rep.Pass() != tc.wantPass || !strings.Contains(out.String(), tc.wantMsg) {
				t.Errorf("pass=%v, want %v; report:\n%s", rep.Pass(), tc.wantPass, out.String())
			}
			if len(rep.Results) != 5 {
				t.Errorf("ran %d checks, want 5", len(rep.Results))
			}
		})
	}
}

func TestRunCancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	o := Options{URL: srv.URL, Checks: 100, Interval: 20 * time.Millisecond, Timeout: time.Second, MaxP95: time.Second}
	rep := Run(ctx, srv.Client(), o)

	if rep.Pass() || !strings.Contains(rep.Problems[0], "interrupted") {
		t.Errorf("a cancelled verify must fail, got %+v", rep.Problems)
	}
}

func TestPercentile(t *testing.T) {
	ms := func(vs ...int) []time.Duration {
		var out []time.Duration
		for _, v := range vs {
			out = append(out, time.Duration(v)*time.Millisecond)
		}
		return out
	}
	tests := []struct {
		in   []time.Duration
		p    float64
		want time.Duration
	}{
		{nil, 0.95, 0},
		{ms(42), 0.95, 42 * time.Millisecond},
		// Same data as the store test, same answers as Postgres percentile_cont.
		{ms(1000, 100, 900, 200, 800, 300, 700, 400, 600, 500), 0.50, 550 * time.Millisecond},
		{ms(1000, 100, 900, 200, 800, 300, 700, 400, 600, 500), 0.95, 955 * time.Millisecond},
	}
	for _, tc := range tests {
		if got := Percentile(tc.in, tc.p); got != tc.want {
			t.Errorf("Percentile(%v, %v) = %v, want %v", tc.in, tc.p, got, tc.want)
		}
	}
}
