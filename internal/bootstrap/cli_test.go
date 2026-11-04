package bootstrap

import (
	"bytes"
	"strings"
	"testing"
)

func TestCLI(t *testing.T) {
	for _, args := range [][]string{nil, {"help"}, {"--help"}, {"-h"}} {
		var out, err bytes.Buffer
		if c := RunCLI(args, &out, &err); c != 0 || out.String() != helpText || err.Len() != 0 {
			t.Fatalf("help: %d %q %q", c, out.String(), err.String())
		}
	}
	var out, err bytes.Buffer
	if c := RunCLI([]string{"version"}, &out, &err); c != 0 || !strings.HasPrefix(out.String(), "ZaH source (") {
		t.Fatalf("version: %d %s", c, out.String())
	}
	for _, a := range [][]string{{"unknown"}, {"version", "extra"}, {"doctor", "bad"}, {"rpc"}, {"launch"}} {
		out.Reset()
		err.Reset()
		if c := RunCLI(a, &out, &err); c == 0 || err.Len() == 0 {
			t.Fatalf("expected invalid command: %v", a)
		}
	}
}
