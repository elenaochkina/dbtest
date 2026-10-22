package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	tea "charm.land/bubbletea/v2"

	"github.com/elenaochkina/dbtest/internal/console"
	"github.com/elenaochkina/dbtest/internal/logbus"
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

	serviceLauncher, err := stack.New(logs.Handler())
	if err != nil {
		return err
	}
	serviceLauncher.Start(context.Background())
	defer serviceLauncher.Close()

	_, err = tea.NewProgram(console.New(serviceLauncher, logs)).Run()
	return err
}
