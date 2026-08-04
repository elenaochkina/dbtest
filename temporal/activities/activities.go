// Package activities holds the side-effecting step logic the workflows schedule.
package activities

import "github.com/elenaochkina/dbtest/harness"

// ContainerInput targets a container a Start activity returned.
type ContainerInput struct {
	Runner harness.RunnerName
	Handle harness.Handle
}
