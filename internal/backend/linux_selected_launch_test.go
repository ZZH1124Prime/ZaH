package backend

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/ZZH1124Prime/ZaH/internal/process"
	"github.com/ZZH1124Prime/ZaH/internal/recipe"
)

func TestSelectedLinuxDesktopUsesOriginalNativeEntry(t *testing.T) {
	r, id, replies := linuxFixture(t)
	selected := LinuxProgramCandidate{Name: "Editor", Kind: "desktop", Path: "/usr/share/applications/editor.desktop", CWD: "/tmp", SHA256: strings.Repeat("a", 64)}
	r.Recipe.LinuxEntry = &recipe.LinuxEntry{Kind: selected.Kind, Path: selected.Path, CWD: selected.CWD, SHA256: selected.SHA256}
	raw, _ := json.Marshal([]LinuxProgramCandidate{selected})
	f := &linuxFixtureRunner{replies: append(replies, process.Result{Stdout: string(raw)}, process.Result{})}
	args, e := (Adapter{f}).selectedLinuxCommand(context.Background(), r, id)
	if e != nil || !reflect.DeepEqual(args, []string{"/usr/bin/gio", "launch", selected.Path}) {
		t.Fatal(args, e)
	}
	r.Recipe.PendingEntry = true
	before := len(f.calls)
	if _, e = (Adapter{f}).selectedLinuxCommand(context.Background(), r, id); e == nil || len(f.calls) != before {
		t.Fatal("unconfirmed application launched")
	}
}
