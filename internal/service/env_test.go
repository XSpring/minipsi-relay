package service

import (
	"strings"
	"testing"
)

func TestORCFromEnv(t *testing.T) {
	t.Setenv(EnvCCID, "cc-1")
	t.Setenv(EnvORCURL, "http://orc:8080")
	id, u, err := ORCFromEnv()
	if err != nil || id != "cc-1" || u != "http://orc:8080" {
		t.Fatalf("got %q %q %v", id, u, err)
	}

	t.Setenv(EnvCCID, "")
	t.Setenv(EnvORCURL, "")
	_, _, err = ORCFromEnv()
	if err == nil || !strings.Contains(err.Error(), EnvCCID) || !strings.Contains(err.Error(), EnvORCURL) {
		t.Errorf("both missing: %v", err)
	}

	t.Setenv(EnvCCID, "bad id SECRET")
	t.Setenv(EnvORCURL, "ftp://SECRET")
	_, _, err = ORCFromEnv()
	if err == nil || !strings.Contains(err.Error(), EnvCCID) || !strings.Contains(err.Error(), EnvORCURL) {
		t.Errorf("both invalid: %v", err)
	}
	if strings.Contains(err.Error(), "SECRET") {
		t.Errorf("error repeats a value: %v", err)
	}
}
