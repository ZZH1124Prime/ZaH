package host

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestRuntimeChecksOnlyExistingReadOnlyCommands(t *testing.T) {
	report := Report{OS: "linux", Tools: []ToolCheck{{Name: "docker", Available: true}, {Name: "distrobox", Available: true}, {Name: "flatpak", Available: false}}}
	var calls [][]string
	got := inspectRuntime(context.Background(), report, func(ctx context.Context, p string, args ...string) (string, error) {
		calls = append(calls, append([]string{p}, args...))
		if p == "docker" {
			return "credentials must not be surfaced", errors.New("denied")
		}
		return "version", nil
	})
	want := [][]string{{"docker", "info", "--format", "{{.ServerVersion}}"}, {"distrobox", "--version"}}
	if !reflect.DeepEqual(calls, want) || len(got.Checks) != 3 || got.Checks[0].Available || !got.Checks[1].Available || got.Checks[2].Available {
		t.Fatal(calls, got)
	}
	if got.Checks[0].Detail == "credentials must not be surfaced" {
		t.Fatal("raw output leaked")
	}
}
