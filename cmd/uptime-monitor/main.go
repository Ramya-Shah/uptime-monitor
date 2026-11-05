// Command uptime-monitor checks a list of sites on an interval.
//
//	uptime-monitor run -config sites.yaml
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/Ramya-Shah/uptime-monitor/internal/checker"
	"github.com/Ramya-Shah/uptime-monitor/internal/config"
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
	default:
		err = fmt.Errorf("unknown command %q (want: run)", cmd)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	cfgPath := fs.String("config", envOr("CONFIG_PATH", "sites.yaml"), "path to sites YAML")
	fs.Parse(args)

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}

	// ctx is cancelled on Ctrl-C (SIGINT) or SIGTERM (docker stop, Kubernetes).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	log.Info("starting", "sites", len(cfg.Sites))

	results := make(chan checker.Result)
	go checker.Run(ctx, &http.Client{}, cfg.Sites, results)

	// Drain until Run closes the channel, which happens only after
	// every in-flight check has finished.
	for r := range results {
		attrs := []any{"site", r.Site, "status", r.Status, "latency_ms", r.Latency.Milliseconds(), "ok", r.OK}
		if r.OK {
			log.Info("check", attrs...)
		} else {
			log.Warn("check", append(attrs, "err", r.Err)...)
		}
	}
	log.Info("stopped cleanly")
	return nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
