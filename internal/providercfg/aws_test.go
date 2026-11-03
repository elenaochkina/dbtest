package providercfg

import (
	"context"
	"testing"
)

func TestCheckAWSReportsMissingVariables(t *testing.T) {
	for _, name := range []string{"AWS_REGION", "AWS_RDS_SECURITY_GROUP_IDS", "AWS_ECS_CLUSTER", "AWS_ECS_SUBNET_IDS", "AWS_ECS_SECURITY_GROUP_IDS", "AWS_ECS_LOG_GROUP"} {
		t.Setenv(name, "set")
	}
	t.Setenv("AWS_ECS_CLUSTER", "")
	t.Setenv("AWS_ECS_LOG_GROUP", "")

	status := CheckAWS(context.Background())
	want := Status{State: StateMisconfigured, Detail: "2 variables missing: run source .env (AWS_ECS_CLUSTER, AWS_ECS_LOG_GROUP)"}
	if status != want {
		t.Fatalf("status = %+v, want %+v", status, want)
	}
}

func TestRoleFromARN(t *testing.T) {
	for arn, want := range map[string]string{
		"arn:aws:sts::123456789012:assumed-role/dbtest/botocore-session-1": "role dbtest",
		"arn:aws:iam::123456789012:user/dbtest-dev":                        "arn:aws:iam::123456789012:user/dbtest-dev",
	} {
		if got := roleFromARN(arn); got != want {
			t.Errorf("roleFromARN(%q) = %q, want %q", arn, got, want)
		}
	}
}

func TestProfileFlag(t *testing.T) {
	t.Setenv("AWS_PROFILE", "elena-dbtest")
	if got := profileFlag(); got != " --profile elena-dbtest" {
		t.Errorf("profileFlag() = %q", got)
	}
	t.Setenv("AWS_PROFILE", "")
	if got := profileFlag(); got != "" {
		t.Errorf("profileFlag() = %q, want empty", got)
	}
}
