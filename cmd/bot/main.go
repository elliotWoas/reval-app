// Command bot runs the Telegram task-manager bot.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	_ "time/tzdata" // timezone data even on hosts without it

	"taskbot/internal/app"
	"taskbot/internal/config"
	"taskbot/internal/platform/logger"
)

func main() {
	cfgPath := flag.String("config", envOr("CONFIG_PATH", "configs/defaults.yaml"), "path to the defaults YAML")
	envFile := flag.String("env", ".env", "optional .env file (ignored when missing)")
	flag.Parse()

	if err := loadDotEnv(*envFile); err != nil {
		fmt.Fprintln(os.Stderr, "env file:", err)
		os.Exit(1)
	}
	cfg, err := config.Load(*cfgPath)
	if err == nil {
		err = cfg.Validate()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	log := logger.New(cfg.App.LogLevel)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := app.Run(ctx, cfg, log); err != nil && ctx.Err() == nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// loadDotEnv sets KEY=VALUE lines from a file without overriding variables already set.
func loadDotEnv(path string) error {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(strings.TrimPrefix(k, "export "))
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		if _, set := os.LookupEnv(k); !set {
			_ = os.Setenv(k, v)
		}
	}
	return sc.Err()
}
