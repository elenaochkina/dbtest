package main

import (
	"os"
	"path/filepath"
	"testing"
)

// unset clears name for the test and restores it afterwards.
func unset(t *testing.T, name string) {
	t.Setenv(name, "")
	os.Unsetenv(name)
}

func TestLoadEnvSetsUnsetVariables(t *testing.T) {
	unset(t, "DBTEST_REGION")
	unset(t, "DBTEST_GROUPS")
	t.Setenv("DBTEST_PROFILE", "from-terminal")
	path := filepath.Join(t.TempDir(), ".env")
	content := "# comment\n\nexport DBTEST_REGION=us-west-2\nDBTEST_GROUPS=\"sg-1,sg-2\"\nexport DBTEST_PROFILE=from-file\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	count, err := loadEnv(path)
	if err != nil || count != 2 {
		t.Fatalf("loadEnv = %d, %v, want 2, nil", count, err)
	}
	for name, want := range map[string]string{"DBTEST_REGION": "us-west-2", "DBTEST_GROUPS": "sg-1,sg-2", "DBTEST_PROFILE": "from-terminal"} {
		if got := os.Getenv(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}

func TestLoadEnvMissingFile(t *testing.T) {
	count, err := loadEnv(filepath.Join(t.TempDir(), ".env"))
	if err != nil || count != 0 {
		t.Fatalf("loadEnv = %d, %v, want 0, nil", count, err)
	}
}
