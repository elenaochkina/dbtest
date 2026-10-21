package main

import (
	"context"
	"fmt"
	"os"

	tea "charm.land/bubbletea/v2"

	"github.com/elenaochkina/dbtest/internal/console"
	"github.com/elenaochkina/dbtest/internal/stack"
)

// dbtest runs the interactive console.
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "dbtest:", err)
		os.Exit(1)
	}
}

// run starts the local stack, runs the console until it quits, then stops
// the stack's checks.
func run() error {
	orchestrator, err := stack.New()
	if err != nil {
		return err
	}
	orchestrator.Start(context.Background())
	defer orchestrator.Close()

	_, err = tea.NewProgram(console.New(orchestrator)).Run()
	return err
}
