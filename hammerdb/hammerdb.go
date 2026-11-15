// Package hammerdb runs HammerDB's TPROC-C workload through hammerdbcli.
package hammerdb

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Config holds the parameters for a TPROC-C build and run.
type Config struct {
	Warehouses int
	Clients    int // virtual users, one connection each
	RampUp     time.Duration
	Duration   time.Duration
}

// Initialize builds the TPROC-C schema: the tpcc user and database, tables,
// data, indexes, stored procedures and statistics.
func Initialize(ctx context.Context, dsn string, cfg Config) error {
	connection, password, err := settings(dsn)
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "hammerdb")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	script := connection + fmt.Sprintf(`diset tpcc pg_count_ware %d
diset tpcc pg_num_vu %d
buildschema
`, cfg.Warehouses, min(runtime.GOMAXPROCS(0), cfg.Warehouses))
	out, err := cli(ctx, dir, script, password)
	if err != nil {
		return err
	}
	if !strings.Contains(out, "TPCC SCHEMA COMPLETE") {
		return fmt.Errorf("hammerdb build did not complete\n%s", tail(out))
	}
	return nil
}

// Run runs the timed TPROC-C driver: RampUp, then Duration measured.
func Run(ctx context.Context, dsn string, cfg Config) (Result, error) {
	rampUp, err := minutes(cfg.RampUp, 0)
	if err != nil {
		return Result{}, err
	}
	duration, err := minutes(cfg.Duration, 1)
	if err != nil {
		return Result{}, err
	}
	connection, password, err := settings(dsn)
	if err != nil {
		return Result{}, err
	}
	dir, err := os.MkdirTemp("", "hammerdb")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(dir)

	script := connection + fmt.Sprintf(`diset tpcc pg_driver timed
diset tpcc pg_total_iterations 2147483647
diset tpcc pg_rampup %d
diset tpcc pg_duration %d
diset tpcc pg_allwarehouse true
diset tpcc pg_timeprofile true
loadscript
vuset vu %d
vucreate
set jobid [ lindex [ split [ vurun ] = ] 1 ]
vudestroy
puts "DBTEST TIMING"
job $jobid timing
`, rampUp, duration, cfg.Clients)
	out, err := cli(ctx, dir, script, password)
	if err != nil {
		return Result{}, err
	}
	return parseResult(out)
}

// settings returns the hammerdbcli lines that select PostgreSQL TPROC-C with
// stored procedures and connect as the DSN's user, which creates and owns the
// tpcc user and database. It also returns the DSN's password.
func settings(dsn string) (string, string, error) {
	u, err := url.Parse(dsn)
	if err != nil || u.Hostname() == "" {
		return "", "", errors.New("hammerdb needs a postgres:// DSN with a host")
	}
	password, _ := u.User.Password()
	port := cmp.Or(u.Port(), "5432")
	sslmode := cmp.Or(u.Query().Get("sslmode"), "prefer")
	database := cmp.Or(strings.TrimPrefix(u.Path, "/"), "postgres")
	for _, value := range []string{u.Hostname(), port, sslmode, u.User.Username(), password, database} {
		if strings.ContainsAny(value, "{}\\\n") {
			return "", "", errors.New("hammerdb DSN contains a brace, backslash or newline")
		}
	}
	return fmt.Sprintf(`dbset db pg
dbset bm TPC-C
diset connection pg_host {%s}
diset connection pg_port {%s}
diset connection pg_sslmode {%s}
diset tpcc pg_superuser {%s}
diset tpcc pg_superuserpass {%s}
diset tpcc pg_defaultdbase {%s}
diset tpcc pg_user tpcc
diset tpcc pg_pass {%s}
diset tpcc pg_dbase tpcc
diset tpcc pg_storedprocs true
`, u.Hostname(), port, sslmode, u.User.Username(), password, database, password), password, nil
}

// cli runs script with hammerdbcli and returns its output with password masked.
func cli(ctx context.Context, dir, script, password string) (string, error) {
	path := filepath.Join(dir, "script.tcl")
	if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, "/opt/hammerdb/hammerdbcli", "auto", path)
	cmd.Dir = "/opt/hammerdb"
	raw, err := cmd.CombinedOutput()
	out := string(raw)
	if password != "" {
		out = strings.ReplaceAll(out, password, "***")
	}
	if err != nil {
		return "", fmt.Errorf("hammerdbcli: %w\n%s", err, tail(out))
	}
	return out, nil
}

// minutes converts d to whole minutes and requires at least least of them.
func minutes(d time.Duration, least int) (int, error) {
	if d%time.Minute != 0 || int(d/time.Minute) < least {
		return 0, fmt.Errorf("hammerdb needs whole minutes, at least %d, got %s", least, d)
	}
	return int(d / time.Minute), nil
}

// tail returns the last 50 lines of out.
func tail(out string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	return strings.Join(lines[max(0, len(lines)-50):], "\n")
}
