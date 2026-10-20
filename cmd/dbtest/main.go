package main

import (
	"fmt"
	"os"

	tea "charm.land/bubbletea/v2"

	"github.com/elenaochkina/dbtest/internal/console"
)

// dbtest runs the interactive console.
func main() {
	if _, err := tea.NewProgram(console.New()).Run(); err != nil {
		fmt.Fprintln(os.Stderr, "dbtest:", err)
		os.Exit(1)
	}
}
