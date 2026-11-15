package hammerdb

import (
	"strings"
	"testing"
	"time"
)

func TestSettings(t *testing.T) {
	script, password, err := settings("postgres://admin:secret@10.0.0.5:5433/app?sslmode=require")
	if err != nil {
		t.Fatal(err)
	}
	if password != "secret" {
		t.Errorf("password = %q, want secret", password)
	}
	for _, line := range []string{
		"diset connection pg_host {10.0.0.5}",
		"diset connection pg_port {5433}",
		"diset connection pg_sslmode {require}",
		"diset tpcc pg_superuser {admin}",
		"diset tpcc pg_superuserpass {secret}",
		"diset tpcc pg_defaultdbase {app}",
		"diset tpcc pg_pass {secret}",
	} {
		if !strings.Contains(script, line) {
			t.Errorf("script has no %q", line)
		}
	}

	script, _, err = settings("postgres://admin:secret@db/")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{
		"diset connection pg_port {5432}",
		"diset connection pg_sslmode {prefer}",
		"diset tpcc pg_defaultdbase {postgres}",
	} {
		if !strings.Contains(script, line) {
			t.Errorf("default script has no %q", line)
		}
	}
}

func TestSettingsRejects(t *testing.T) {
	for _, dsn := range []string{
		"postgres://admin:se}cret@db/postgres",
		"not a url",
		"postgres:///postgres",
	} {
		_, _, err := settings(dsn)
		if err == nil {
			t.Errorf("settings(%q) returned no error", dsn)
		} else if strings.Contains(err.Error(), "se}cret") {
			t.Errorf("error %q contains the password", err)
		}
	}
}

func TestMinutes(t *testing.T) {
	for _, tc := range []struct {
		d     time.Duration
		least int
		want  int
		ok    bool
	}{
		{10 * time.Minute, 1, 10, true},
		{0, 0, 0, true},
		{0, 1, 0, false},
		{30 * time.Second, 1, 0, false},
		{90 * time.Second, 1, 0, false},
	} {
		got, err := minutes(tc.d, tc.least)
		if (err == nil) != tc.ok || got != tc.want {
			t.Errorf("minutes(%s, %d) = %d, %v; want %d, ok %v", tc.d, tc.least, got, err, tc.want, tc.ok)
		}
	}
}

func TestParseResult(t *testing.T) {
	output := `Vuser 1:4 Active Virtual Users configured
Vuser 1:TEST RESULT : System achieved 124874 NOPM from 289695 PostgreSQL TPM
Vuser 1:FINISHED SUCCESS
ALL VIRTUAL USERS COMPLETE
vudestroy success
DBTEST TIMING
{
  "NEWORD": {"elapsed_ms": "59377.0", "calls": "126198", "p99_ms": "3.147", "p95_ms": "1.112", "p50_ms": "0.423", "ratio_pct": "32.597"},
  "PAYMENT": {"elapsed_ms": "59377.0", "calls": "126153", "p99_ms": "3.014", "p95_ms": "0.923", "p50_ms": "0.317", "ratio_pct": "27.262"}
}

`
	result, err := parseResult(output)
	if err != nil {
		t.Fatal(err)
	}
	if result.NOPM != 124874 || result.TPM != 289695 {
		t.Errorf("NOPM, TPM = %v, %v; want 124874, 289695", result.NOPM, result.TPM)
	}
	if got := result.Timing["NEWORD"]; got.Calls != 126198 || got.P95Ms != 1.112 {
		t.Errorf("NEWORD = %+v", got)
	}

	metrics := result.Metrics()
	for key, want := range map[string]float64{"nopm": 124874, "tpm": 289695, "neword_p95_ms": 1.112, "payment_p50_ms": 0.317} {
		if metrics[key] != want {
			t.Errorf("metrics[%s] = %v, want %v", key, metrics[key], want)
		}
	}

	if _, err := parseResult("Vuser 1:FINISHED SUCCESS"); err == nil {
		t.Error("parseResult without a TEST RESULT line returned no error")
	}
	if _, err := parseResult("Vuser 1:TEST RESULT : System achieved 1 NOPM from 2 PostgreSQL TPM"); err == nil {
		t.Error("parseResult without timing returned no error")
	}
}
