package provider

import (
	"fmt"
)

// PGTarget is provider-agnostic connection info for a Postgres instance.
type PGTarget struct {
	Host     string
	Port     int
	Database string
	User     string
}

// Addr returns the "host:port" pair.
func (t PGTarget) Addr() string {
	return fmt.Sprintf("%s:%d", t.Host, t.Port)
}

// URL renders a pgx/libpq URL DSN with the password injected. Build it only at
// the moment of connecting, so the secret never has to live alongside the target.
func (t PGTarget) URL(password string) string {
	return fmt.Sprintf("postgres://%s:%s@%s:%d/%s", t.User, password, t.Host, t.Port, t.Database)
}

// ClusterInfo is returned by Provision and used to connect and deprovision.
type ClusterInfo struct {
	ID       string
	Target   PGTarget // how the worker reaches the database
	Internal PGTarget // a sibling container reaches the same database.
	Password string   // rendered into a DSN at connect time, never persisted
}

type ProvisionRequest struct {
	VCPU             float64
	MemoryMiB        int
	DiskGiB          int
	PostgresVersion  string
	HighAvailability bool
}

// DBServiceName identifies a database service.
type DBServiceName string

const (
	Docker DBServiceName = "docker"
	RDS    DBServiceName = "rds"
	Aurora DBServiceName = "aurora"
)

// CloudName identifies a cloud.
type CloudName string

const (
	Local CloudName = "local"
	AWS   CloudName = "aws"
	GCP   CloudName = "gcp"
)

// State is whether a cloud can be used.
type State string

const (
	StateReady         State = "ready"
	StateNeedsLogin    State = "needs login"
	StateMisconfigured State = "misconfigured"
)

// Status is a cloud's state and a line explaining it.
type Status struct {
	State  State
	Detail string
}
