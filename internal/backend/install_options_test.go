package backend

import (
	"context"
	"github.com/ZZH1124Prime/ZaH/internal/process"
	"strings"
	"testing"
)

func TestInstallOptionsNeverPullsOrCreates(t *testing.T) {
	f := &fakeRunner{result: process.Result{Stdout: `{"Repository":"ubuntu","Tag":"22.04","Digest":"sha256:` + strings.Repeat("a", 64) + `"}
{"Repository":"ubuntu","Tag":"26.04","Digest":"sha256:` + strings.Repeat("c", 64) + `"}
{"Repository":"unrelated","Tag":"latest","Digest":"sha256:` + strings.Repeat("b", 64) + `"}`}}
	values, e := (Adapter{Runner: f}).InstallOptions(context.Background(), "linux")
	if e != nil || len(values) != 2 || !strings.Contains(values[0].Label, "22.04") || !strings.Contains(values[1].Label, "26.04") || !values[1].Default || values[0].Default {
		t.Fatal(values, e)
	}
	if len(f.calls) != 1 || strings.Join(f.calls[0].Args, " ") != "image ls --digests --no-trunc --format {{json .}}" {
		t.Fatal(f.calls)
	}
	f.result = process.Result{Stdout: `{"runners":["soda-9.0","sys-wine","proton-test","../escape"]}`}
	values, e = (Adapter{Runner: f}).InstallOptions(context.Background(), "windows")
	if e != nil || len(values) != 1 || values[0].Value != "soda-9.0" {
		t.Fatal(values, e)
	}
}
