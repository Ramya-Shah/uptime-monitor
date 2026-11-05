// Command uptime-monitor checks a list of sites on an interval,
// stores results in Postgres and serves a JSON API and status page.
//
//	uptime-monitor run -config sites.yaml       (needs DATABASE_URL)
//	uptime-monitor verify --url URL [flags]     (post-deploy check for CI)
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Ramya-Shah/uptime-monitor/internal/api"
	"github.com/Ramya-Shah/uptime-monitor/internal/checker"
	"github.com/Ramya-Shah/uptime-monitor/internal/config"
	"github.com/Ramya-Shah/uptime-monitor/internal/store"
	"github.com/Ramya-Shah/uptime-monitor/internal/verify"
)

func main() {
	cmd, args := "run", os.Args[1:]
	if len(args) > 0 && args[0][0] != '-' {
		cmd, args = args[0], args[1:]
	}

	var err error
	switch cmd {
	case "run":
		err = run(args)
	case "verify":
		os.Exit(verifyCmd(args)) // 0 healthy, 1 unhealthy, 2 bad usage
	default:
		err = fmt.Errorf("unknown command %q (want: run, verify)", cmd)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	cfgPath := fs.String("config", envOr("CONFIG_PATH", "sites.yaml"), "path to sites YAML")
	addr := fs.String("addr", envOr("ADDR", ":8080"), "HTTP listen address")
	fs.Parse(args)

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		return errors.New("DATABASE_URL is not set")
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}

	// ctx is cancelled on Ctrl-C (SIGINT) or SIGTERM (docker stop, Kubernetes).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log := slog.New(slog.NewTextHandler(os.Stdout, nil))

	st, err := store.Open(ctx, dbURL)
	if err != nil {
		return err
	}
	defer st.Close()

	srv := &http.Server{Addr: *addr, Handler: api.New(st, cfg.Sites), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("http server", "err", err)
			stop() // can't serve the API: shut the whole process down
		}
	}()
	log.Info("starting", "sites", len(cfg.Sites), "addr", *addr)

	results := make(chan checker.Result)
	go checker.Run(ctx, checker.NewClient(), cfg.Sites, results)

	// Drain until Run closes the channel (after in-flight checks finish).
	// Inserts use their own short timeout, not ctx, so the last results
	// are still saved during shutdown.
	for r := range results {
		attrs := []any{"site", r.Site, "status", r.Status, "latency_ms", r.Latency.Milliseconds(), "ok", r.OK}
		if r.OK {
			log.Info("check", attrs...)
		} else {
			log.Warn("check", append(attrs, "err", r.Err)...)
		}
		ictx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := st.Insert(ictx, r); err != nil {
			log.Error("store", "err", err)
		}
		cancel()
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil { // waits for open API requests
		return err
	}
	log.Info("stopped cleanly")
	return nil
}

// verifyCmd is the CI release check:
//
//	uptime-monitor verify --url https://host/healthz --checks 10 --interval 3s --max-p95 800ms
func verifyCmd(args []string) int {
	fs := flag.NewFlagSet("verify", flag.ExitOnError) // bad flags exit 2
	var o verify.Options
	fs.StringVar(&o.URL, "url", "", "URL to poll (required)")
	fs.IntVar(&o.Checks, "checks", 10, "number of checks")
	fs.DurationVar(&o.Interval, "interval", 3*time.Second, "time between checks")
	fs.DurationVar(&o.Timeout, "timeout", 5*time.Second, "timeout per check")
	fs.DurationVar(&o.MaxP95, "max-p95", 800*time.Millisecond, "fail if p95 latency is above this")
	fs.Parse(args)

	if o.URL == "" || o.Checks < 1 || o.Interval < 0 || o.Timeout <= 0 || o.MaxP95 <= 0 {
		fmt.Fprintln(os.Stderr, "usage: uptime-monitor verify --url URL [--checks N] [--interval D] [--timeout D] [--max-p95 D]")
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	rep := verify.Run(ctx, checker.NewClient(), o)
	rep.Print(os.Stdout)
	if !rep.Pass() {
		return 1
	}
	return 0
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
