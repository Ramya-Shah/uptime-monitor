package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/Ramya-Shah/uptime-monitor/internal/checker"
	"github.com/Ramya-Shah/uptime-monitor/internal/config"
	"github.com/Ramya-Shah/uptime-monitor/internal/store"
)

func TestParseWindow(t *testing.T) {
	tests := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{"", 24 * time.Hour, false},
		{"24h", 24 * time.Hour, false},
		{"7d", 7 * 24 * time.Hour, false},
		{"90m", 90 * time.Minute, false},
		{"0h", 0, true},
		{"-1h", 0, true},
		{"91d", 0, true},
		{"abc", 0, true},
		{"xd", 0, true},
	}
	for _, tc := range tests {
		got, err := ParseWindow(tc.in)
		if (err != nil) != tc.wantErr || got != tc.want {
			t.Errorf("ParseWindow(%q) = %v, %v; want %v, err=%v", tc.in, got, err, tc.want, tc.wantErr)
		}
	}
}

// TestAPI runs the real handler against the real database.
func TestAPI(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; run `docker compose up -d db`")
	}
	ctx := context.Background()
	st, err := store.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	site := fmt.Sprintf("api-%d", time.Now().UnixNano())
	st.Insert(ctx, checker.Result{Site: site, At: time.Now().Add(-time.Minute), Status: 200, Latency: 120 * time.Millisecond, OK: true})
	st.Insert(ctx, checker.Result{Site: site, At: time.Now(), Status: 500, Latency: 80 * time.Millisecond, Err: "500 Internal Server Error"})

	h := New(st, []config.Site{{Name: site, URL: "https://example.test", Interval: 30 * time.Second}})

	tests := []struct {
		path     string
		wantCode int
		check    func(t *testing.T, body []byte)
	}{
		{"/healthz", 200, nil},
		{"/api/sites", 200, func(t *testing.T, b []byte) {
			var sites []siteJSON
			json.Unmarshal(b, &sites)
			if len(sites) != 1 || sites[0].LastCheck == nil || sites[0].LastCheck.Status != 500 {
				t.Errorf("unexpected body %s", b)
			}
		}},
		{"/api/sites/" + site + "/stats?window=24h", 200, func(t *testing.T, b []byte) {
			var s store.Stats
			json.Unmarshal(b, &s)
			if s.Checks != 2 || s.Failures != 1 || *s.UptimePct != 50 || *s.P95Ms != 120 || s.Window != "24h" {
				t.Errorf("unexpected body %s", b)
			}
		}},
		{"/", 200, func(t *testing.T, b []byte) {
			for _, want := range []string{site, "1 of 1 sites down", `class="pill down"`, "50.00%", "120 ms", "500 Internal Server Error"} {
				if !bytes.Contains(b, []byte(want)) {
					t.Errorf("status page missing %q", want)
				}
			}
		}},
		{"/no-such-page", 404, nil}, // {$} keeps "/" from matching everything
		{"/api/sites/nope/stats", 404, nil},
		{"/api/sites/" + site + "/stats?window=banana", 400, nil},
		{"/api/sites", 405, nil}, // POST below: Go 1.22 routing rejects the wrong method
	}
	for i, tc := range tests {
		method := http.MethodGet
		if i == len(tests)-1 {
			method = http.MethodPost
		}
		t.Run(method+" "+tc.path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(method, tc.path, nil))
			if rec.Code != tc.wantCode {
				t.Fatalf("code %d, want %d; body %s", rec.Code, tc.wantCode, rec.Body)
			}
			if tc.check != nil {
				tc.check(t, rec.Body.Bytes())
			}
		})
	}
}
