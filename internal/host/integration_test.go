package host

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBindingDetectsReplacement(t *testing.T) {
	p := filepath.Join(t.TempDir(), "mount")
	if err := os.Mkdir(p, 0700); err != nil {
		t.Fatal(err)
	}
	b, err := InspectBinding(p, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = VerifyBinding(b); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(p, p+"-old"); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(p, 0700); err != nil {
		t.Fatal(err)
	}
	if VerifyBinding(b) == nil {
		t.Fatal("accepted replacement mount")
	}
}
func TestDesktopEntryEscapingAndNoOverwrite(t *testing.T) {
	d := t.TempDir()
	p, err := InstallDesktopEntry(d, "/opt/空 格/zah", "01234567-abcd", "音乐播放器")
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p)
	if err != nil || !strings.Contains(string(b), `Exec="/opt/空 格/zah" launch 01234567-abcd`) {
		t.Fatalf("%s %v", b, err)
	}
	if _, err = InstallDesktopEntry(d, "/opt/zah", "01234567-abcd", "new"); err == nil {
		t.Fatal("overwrote existing entry")
	}
	if _, err = DesktopEntry("/opt/zah", "01234567-abcd", "x\nExec=evil"); err == nil {
		t.Fatal("accepted newline injection")
	}
}
