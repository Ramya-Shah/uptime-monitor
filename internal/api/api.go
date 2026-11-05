// Package api serves the monitor's JSON endpoints using Go 1.22 method+path routing.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Ramya-Shah/uptime-monitor/internal/config"
	"github.com/Ramya-Shah/uptime-monitor/internal/store"
)

type server struct {
	store *store.Store
	sites []config.Site
}

// New returns the HTTP handler with all routes registered.
func New(st *store.Store, sites []config.Site) http.Handler {
	s := &server{store: st, sites: sites}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/sites", s.listSites)
	mux.HandleFunc("GET /api/sites/{name}/stats", s.siteStats)
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /{$}", s.statusPage) // {$} = exactly "/", not every path
	return mux
}

type lastCheck struct {
	At        time.Time `json:"at"`
	Status    int       `json:"status"`
	LatencyMs int64     `json:"latency_ms"`
	OK        bool      `json:"ok"`
	Error     string    `json:"error,omitempty"`
}

type siteJSON struct {
	Name      string     `json:"name"`
	URL       string     `json:"url"`
	Interval  string     `json:"interval"`
	LastCheck *lastCheck `json:"last_check"` // null until the first check is stored
}

func (s *server) listSites(w http.ResponseWriter, r *http.Request) {
	latest, err := s.store.Latest(r.Context())
	if err != nil {
		serverError(w, err)
		return
	}
	out := make([]siteJSON, 0, len(s.sites))
	for _, site := range s.sites {
		sj := siteJSON{Name: site.Name, URL: site.URL, Interval: site.Interval.String()}
		if res, ok := latest[site.Name]; ok {
			sj.LastCheck = &lastCheck{res.At, res.Status, res.Latency.Milliseconds(), res.OK, res.Err}
		}
		out = append(out, sj)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) siteStats(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name") // filled from {name} in the route pattern
	if !s.known(name) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown site " + name})
		return
	}
	window, err := ParseWindow(r.URL.Query().Get("window"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	st, err := s.store.Stats(r.Context(), name, window)
	if err != nil {
		serverError(w, err)
		return
	}
	st.Window = "24h"
	if q := r.URL.Query().Get("window"); q != "" {
		st.Window = q // echo "7d" back as the caller wrote it, not "168h0m0s"
	}
	writeJSON(w, http.StatusOK, st)
}

// healthz reports whether the monitor itself can reach its database.
func (s *server) healthz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.store.Ping(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "db unreachable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *server) known(name string) bool {
	for _, site := range s.sites {
		if site.Name == name {
			return true
		}
	}
	return false
}

// ParseWindow accepts Go durations ("24h", "90m") plus whole days ("7d").
// Empty means 24h. Windows are capped at 90 days.
func ParseWindow(v string) (time.Duration, error) {
	if v == "" {
		return 24 * time.Hour, nil
	}
	var d time.Duration
	if days, ok := strings.CutSuffix(v, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil {
			return 0, fmt.Errorf("bad window %q", v)
		}
		d = time.Duration(n) * 24 * time.Hour
	} else {
		var err error
		if d, err = time.ParseDuration(v); err != nil {
			return 0, fmt.Errorf("bad window %q", v)
		}
	}
	if d <= 0 || d > 90*24*time.Hour {
		return 0, fmt.Errorf("window must be between 0 and 90d")
	}
	return d, nil
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func serverError(w http.ResponseWriter, err error) {
	slog.Error("api", "err", err)
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
}
