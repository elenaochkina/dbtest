// Package providercfg checks whether a cloud provider is ready to use.
package providercfg

// State is whether a provider can be used.
type State string

const (
	StateReady         State = "ready"
	StateNeedsLogin    State = "needs login"
	StateMisconfigured State = "misconfigured"
)

type Status struct {
	State  State
	Detail string
}
