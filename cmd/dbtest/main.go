package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/elenaochkina/dbtest/internal/console"
	"github.com/elenaochkina/dbtest/internal/logbus"
	"github.com/elenaochkina/dbtest/internal/pricing"
	"github.com/elenaochkina/dbtest/internal/stack"
)

// dbtest runs the interactive console.
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "dbtest:", err)
		os.Exit(1)
	}
}

// run sends all logging to a log bus, starts the local stack, runs the
// console until it quits, then stops the stack's checks.
func run() error {
	logs := logbus.New()
	slog.SetDefault(slog.New(logs.Handler()))
	if count, err := loadEnv(".env"); err != nil {
		slog.Warn("load .env failed", "error", err)
	} else if count > 0 {
		slog.Info("loaded .env", "variables", count)
	}

	serviceLauncher, err := stack.New(logs.Handler())
	if err != nil {
		return err
	}
	serviceLauncher.Start(context.Background())
	defer serviceLauncher.Close()

	prices, err := pricing.New(context.Background())
	if err != nil {
		slog.Warn("price estimates unavailable", "error", err)
	}
	_, err = tea.NewProgram(console.New(serviceLauncher, logs, prices)).Run()
	return err
}

// loadEnv sets each variable from the .env file at path that is not already
// set, and returns how many it set. A missing file sets none.
func loadEnv(path string) (int, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	count := 0
	for index, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, value, found := strings.Cut(strings.TrimPrefix(line, "export "), "=")
		if !found {
			return count, fmt.Errorf("%s line %d has no =", path, index+1)
		}
		if _, set := os.LookupEnv(name); set {
			continue
		}
		if err := os.Setenv(name, strings.Trim(value, `"'`)); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}
