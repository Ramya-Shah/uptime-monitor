package checker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Ramya-Shah/uptime-monitor/internal/config"
)

// sleepy returns a handler that waits d (or until the client gives up), then writes status.
func sleepy(d time.Duration, status int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(d):
		case <-r.Context().Done():
			return
		}
		w.WriteHeader(status)
	}
}

func TestCheck(t *testing.T) {
	tests := []struct {
		name       string
		handler    http.HandlerFunc
		timeout    time.Duration
		wantOK     bool
		wantStatus int
		wantErr    string // substring; "" means no error
		minLatency time.Duration
	}{
		{"up", sleepy(0, 200), time.Second, true, 200, "", 0},
		{"slow but within timeout", sleepy(150*time.Millisecond, 200), time.Second, true, 200, "", 150 * time.Millisecond},
		{"server error", sleepy(0, 500), time.Second, false, 500, "500", 0},
		{"timeout", sleepy(2*time.Second, 200), 100 * time.Millisecond, false, 0, "deadline exceeded", 100 * time.Millisecond},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler)
			defer srv.Close()

			site := config.Site{Name: tc.name, URL: srv.URL, Timeout: tc.timeout}
			got := Check(context.Background(), srv.Client(), site)

			if got.OK != tc.wantOK || got.Status != tc.wantStatus {
				t.Errorf("ok=%v status=%d, want ok=%v status=%d", got.OK, got.Status, tc.wantOK, tc.wantStatus)
			}
			if tc.wantErr == "" && got.Err != "" {
				t.Errorf("unexpected err %q", got.Err)
			}
			if tc.wantErr != "" && !strings.Contains(got.Err, tc.wantErr) {
				t.Errorf("err %q, want it to contain %q", got.Err, tc.wantErr)
			}
			if got.Latency < tc.minLatency {
				t.Errorf("latency %v, want >= %v", got.Latency, tc.minLatency)
			}
		})
	}
}

// TestRunShutdown checks that cancelling Run lets an in-flight check finish
// (we still get its result) and then closes the channel.
func TestRunShutdown(t *testing.T) {
	srv := httptest.NewServer(sleepy(300*time.Millisecond, 200))
	defer srv.Close()

	sites := []config.Site{{Name: "a", URL: srv.URL, Interval: time.Minute, Timeout: 5 * time.Second}}
	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan Result)
	go Run(ctx, srv.Client(), sites, out)

	time.Sleep(50 * time.Millisecond) // first check is now in flight
	cancel()

	var got []Result
	for r := range out { // ends only when Run closes out
		got = append(got, r)
	}
	if len(got) != 1 || !got[0].OK {
		t.Fatalf("got %+v, want one successful result from the in-flight check", got)
	}
}
