package recipe

import (
	"strings"
	"testing"
)

func valid() Recipe {
	return Recipe{SchemaVersion: 1, ID: "demo", Revision: "1", Platform: "linux", Architecture: "amd64", Strategy: "appimage", Image: "docker.io/library/ubuntu@sha256:" + strings.Repeat("a", 64), Entry: "squashfs-root/AppRun"}
}
func TestValidation(t *testing.T) {
	r := valid()
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	tests := []func(*Recipe){func(r *Recipe) { r.Image = "ubuntu:latest" }, func(r *Recipe) { r.Dependencies = []string{"x;touch /tmp/pwn"} }, func(r *Recipe) { r.Entry = "../../AppRun" }, func(r *Recipe) { r.Strategy = "shell" }, func(r *Recipe) { r.Architecture = "arm64" }}
	for _, f := range tests {
		x := r
		f(&x)
		if x.Validate() == nil {
			t.Fatal("accepted unsafe recipe")
		}
	}
}
func TestDecodeRejectsUnknownTrailingAndOversize(t *testing.T) {
	for _, s := range []string{`{"command":"sh"}`, `{} {}`, strings.Repeat(" ", MaxBytes+1)} {
		if _, err := Decode(strings.NewReader(s)); err == nil {
			t.Fatal("accepted invalid JSON")
		}
	}
}

func TestExplicitLifecycleScope(t *testing.T) {
	r := valid()
	original := r.Digest()
	r.Lifecycle = "process-tree"
	if e := r.Validate(); e != nil {
		t.Fatal(e)
	}
	if original == r.Digest() {
		t.Fatal("lifecycle must change immutable recipe revision")
	}
	r.Lifecycle = "window-disappeared"
	if r.Validate() == nil {
		t.Fatal("unverified window heuristic accepted")
	}
	r.Lifecycle = "process-tree"
	r.Platform = "windows"
	r.Strategy = "exe"
	r.Runner = "runner"
	r.Image = ""
	r.Entry = "Program"
	if r.Validate() == nil {
		t.Fatal("Linux supervisor declared for Windows")
	}
}
