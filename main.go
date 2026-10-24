//
// Date: 2026-10-24
// Author: Spicer Matthews (spicer@cloudmanic.com)
// Copyright: 2026 Cloudmanic Labs, LLC. All rights reserved.
//

// apple-store-watcher checks Apple Stores for pickup stock on a timer and
// texts through Twilio when a watched product comes in.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
)

// main runs the watcher and exits non-zero on a startup error.
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "apple-store-watcher:", err)
		os.Exit(1)
	}
}

// run loads settings, checks the Twilio keys, and then watches until the
// process is killed.
func run() error {
	configPath := flag.String("config", "config.json", "path to the watcher config")
	envPath := flag.String("env", ".env", "path to the file holding the Twilio keys")
	flag.Parse()

	envLoaded, err := LoadEnvFile(*envPath)
	if err != nil {
		return err
	}

	cfg, err := LoadConfig(*configPath)
	if err != nil {
		return err
	}

	logFile, err := os.OpenFile(cfg.LogFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open log file %s: %w", cfg.LogFile, err)
	}
	defer logFile.Close()

	log := newLogger(io.MultiWriter(os.Stdout, logFile))

	log.Info("apple-store-watcher starting",
		"pid", os.Getpid(),
		"config", *configPath,
		"env_file", *envPath,
		"env_values_loaded", envLoaded,
	)

	cfg.LogSummary(log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	twilio, err := NewTwilioClientFromEnv(log)
	if err != nil {
		log.Error("twilio settings missing", "error", err)
		return err
	}

	name, status, err := twilio.VerifyAccount(ctx)
	if err != nil {
		log.Error("twilio keys did not work", "error", err)
		return err
	}

	log.Info("twilio keys work", "account", name, "account_status", status, "from", twilio.From)

	NewWatcher(cfg, NewAppleClient(log), twilio, log).Run(ctx)

	log.Info("apple-store-watcher stopped")

	return nil
}

// newLogger logs everything, debug included, because the log is how we see
// what the watcher did while nobody was looking.
func newLogger(out io.Writer) *slog.Logger {
	return slog.New(slog.NewTextHandler(out, &slog.HandlerOptions{Level: slog.LevelDebug}))
}
