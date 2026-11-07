package aws

import (
	"context"
	"slices"
	"testing"

	"github.com/elenaochkina/dbtest/harness"
	"github.com/elenaochkina/dbtest/provider"
	"github.com/elenaochkina/dbtest/provider/cloud"
)

func TestAWSCheckReportsMissingVariables(t *testing.T) {
	for _, name := range []string{"AWS_REGION", "AWS_RDS_SECURITY_GROUP_IDS", "AWS_ECS_CLUSTER", "AWS_ECS_SUBNET_IDS", "AWS_ECS_SECURITY_GROUP_IDS", "AWS_ECS_LOG_GROUP"} {
		t.Setenv(name, "set")
	}
	t.Setenv("AWS_ECS_CLUSTER", "")
	t.Setenv("AWS_ECS_LOG_GROUP", "")

	status := (&AWSCloud{}).Check(context.Background())
	want := provider.Status{State: provider.StateMisconfigured, Detail: "2 variables missing: run source .env (AWS_ECS_CLUSTER, AWS_ECS_LOG_GROUP)"}
	if status != want {
		t.Fatalf("status = %+v, want %+v", status, want)
	}
}

func TestAWSRegistersItself(t *testing.T) {
	if !slices.Contains(cloud.Names(), provider.AWS) {
		t.Errorf("cloud.Names() = %v, want aws", cloud.Names())
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

func TestImagesReadTaskDefinitions(t *testing.T) {
	t.Setenv("AWS_ECS_PROBE_TASK_DEFINITION", "other-probe")
	t.Setenv("AWS_ECS_BENCH_TASK_DEFINITION", "")

	want := harness.Images{Probe: "other-probe", Bench: "dbtest-bench"}
	if got := (&AWSCloud{}).Images(harness.Fargate); got != want {
		t.Errorf("Images() = %+v, want %+v", got, want)
	}
}
