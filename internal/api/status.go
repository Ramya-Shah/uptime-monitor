package api

import (
	"bytes"
	_ "embed"
	"fmt"
	"html/template"
	"net/http"
	"time"
)

//go:embed status.html
var statusHTML string

// html/template escapes every value, so an error message from a site
// can't inject HTML into the page.
var statusTmpl = template.Must(template.New("status").Parse(statusHTML))

type statusRow struct {
	Name, URL, State string // State is "up", "down" or "unknown"
	Up24, Up7, P95   string
	Checked          string
	FailAt, FailErr  string
}

type statusPage struct {
	Rows      []statusRow
	Down      int
	Generated string
}

func (s *server) statusPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	latest, err := s.store.Latest(ctx)
	if err != nil {
		serverError(w, err)
		return
	}
	fails, err := s.store.LastFailures(ctx)
	if err != nil {
		serverError(w, err)
		return
	}

	now := time.Now()
	page := statusPage{Generated: now.UTC().Format("2006-01-02 15:04:05 UTC")}
	for _, site := range s.sites {
		day, err := s.store.Stats(ctx, site.Name, 24*time.Hour)
		if err != nil {
			serverError(w, err)
			return
		}
		week, err := s.store.Stats(ctx, site.Name, 7*24*time.Hour)
		if err != nil {
			serverError(w, err)
			return
		}

		row := statusRow{Name: site.Name, URL: site.URL, State: "unknown", Checked: "never",
			Up24: pct(day.UptimePct), Up7: pct(week.UptimePct), P95: ms(day.P95Ms)}
		if last, ok := latest[site.Name]; ok {
			row.State = "down"
			if last.OK {
				row.State = "up"
			}
			row.Checked = ago(now.Sub(last.At))
		}
		if row.State == "down" {
			page.Down++
		}
		if f, ok := fails[site.Name]; ok {
			row.FailAt = f.At.UTC().Format("Jan 2 15:04 UTC") + " (" + ago(now.Sub(f.At)) + ")"
			row.FailErr = f.Err
		}
		page.Rows = append(page.Rows, row)
	}

	// Render into a buffer first so a template error becomes a clean 500
	// instead of a half-written page.
	var buf bytes.Buffer
	if err := statusTmpl.Execute(&buf, page); err != nil {
		serverError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	buf.WriteTo(w)
}

func pct(p *float64) string {
	if p == nil {
		return "—"
	}
	return fmt.Sprintf("%.2f%%", *p)
}

func ms(p *float64) string {
	if p == nil {
		return "—"
	}
	return fmt.Sprintf("%.0f ms", *p)
}

func ago(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
