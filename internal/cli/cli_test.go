package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestVersionCommand(t *testing.T) {
	root := New("v1.2.3-test")
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"version"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != "v1.2.3-test" {
		t.Fatalf("output = %q", out.String())
	}
}

func TestSyncRefusesDaemonAmbiguityWithoutOnce(t *testing.T) {
	root := New("test")
	root.SetArgs([]string{"sync"})
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "requires --once") {
		t.Fatalf("error = %v", err)
	}
}
