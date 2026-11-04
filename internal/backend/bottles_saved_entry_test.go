package backend

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestSavedWindowsEntryPreservesLiteralArguments(t *testing.T) {
	args := []string{"--profile", "a b", "$HOME; echo no", "quote'quote"}
	raw, e := quotedProgramArgs(args)
	if e != nil {
		t.Fatal(e)
	}
	back, e := discoveredProgramArgs(raw)
	if e != nil || !reflect.DeepEqual(args, back) {
		t.Fatal(raw, back, e)
	}
	saved, e := newSavedEntry("/bottle", "/bottle/drive_c/app/app.exe", WindowsEntryOptions{Args: args})
	if e != nil {
		t.Fatal(e)
	}
	row := map[string]json.RawMessage{}
	row["arguments"], _ = json.Marshal(raw)
	row["folder"], _ = json.Marshal("/bottle/drive_c/app")
	if e = saved.verify("/bottle", "/bottle/drive_c/app/app.exe", row); e != nil {
		t.Fatal(e)
	}
	if saved.Path != "drive_c/app/app.exe" || saved.CWD != "drive_c/app" {
		t.Fatal(saved)
	}
	row["arguments"] = json.RawMessage(`"changed"`)
	if saved.verify("/bottle", "/bottle/drive_c/app/app.exe", row) == nil {
		t.Fatal("changed args accepted")
	}
}
