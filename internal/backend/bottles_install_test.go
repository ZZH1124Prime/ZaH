package backend

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/ZZH1124Prime/ZaH/internal/host"
	"github.com/ZZH1124Prime/ZaH/internal/process"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type guidedBottleRunner struct {
	r                         InstallRequest
	root                      string
	created, edited, failWait bool
	programs                  map[string]string
	calls                     []process.Command
}

func (f *guidedBottleRunner) Run(_ context.Context, c process.Command) (process.Result, error) {
	f.calls = append(f.calls, c)
	out := process.Result{}
	args := strings.Join(c.Args, "|")
	prefix := filepath.Join(f.root, name(f.r.InstanceID))
	if len(c.Args) > 6 && c.Args[1] == "--command=python3" {
		if f.failWait && c.Args[6] == "wait" {
			return out, errors.New("prefix still running")
		}
		binding, e := host.InspectBinding(prefix, true)
		if e != nil {
			return out, e
		}
		proof := WineControl{Server: filepath.Join(filepath.Dir(f.root), "runners", f.r.Recipe.Runner, "bin", "wineserver"), SHA256: strings.Repeat("a", 64), WineSHA256: strings.Repeat("b", 64), Prefix: prefix, Device: binding.Device, Inode: binding.Inode, UID: uint64(os.Getuid()), TmpDevice: 3, TmpInode: 4}
		raw, _ := json.Marshal(proof)
		out.Stdout = string(raw)
		return out, nil
	}
	switch {
	case strings.HasSuffix(args, "--json|list|bottles"):
		all := map[string]any{}
		if _, statErr := os.Stat(prefix); f.created && statErr == nil {
			all[name(f.r.InstanceID)] = map[string]any{"Name": name(f.r.InstanceID), "Path": name(f.r.InstanceID), "Arch": "win64", "Runner": f.r.Recipe.Runner, "Creation_Date": "fixture", "Custom_Path": false, "Environment": "Application", "Parameters": map[string]bool{"sandbox": false, "winebridge": !f.edited}, "Environment_Variables": map[string]string{}}
		}
		raw, _ := json.Marshal(all)
		out.Stdout = string(raw)
	case strings.HasSuffix(args, "info|bottles-path"):
		out.Stdout = f.root
	case len(c.Args) > 3 && c.Args[3] == "new":
		if f.created {
			return out, errors.New("duplicate")
		}
		if e := os.MkdirAll(filepath.Join(prefix, "drive_c"), 0700); e != nil {
			return out, e
		}
		f.created = true
	case len(c.Args) > 3 && c.Args[3] == "shell":
		if c.Args[len(c.Args)-1] != `reg add "HKLM\SOFTWARE\Microsoft\SQMClient" /f` {
			return out, errors.New("unexpected compatibility command")
		}
	case len(c.Args) > 3 && c.Args[3] == "reg":
		// Registry setup is scoped to this newly created fixture Bottle.
	case len(c.Args) > 3 && c.Args[3] == "edit":
		f.edited = true
	case len(c.Args) > 3 && c.Args[3] == "add":
		f.programs[c.Args[7]] = c.Args[9]
	case strings.Contains(args, "--json|programs|-b|"):
		programs := []map[string]any{}
		for name, path := range f.programs {
			programs = append(programs, map[string]any{"name": name, "path": path, "arguments": "", "pre_script": nil, "post_script": "", "launch_options": ""})
		}
		raw, _ := json.Marshal(programs)
		out.Stdout = string(raw)
	default:
		return out, errors.New("unexpected fixture command: " + args)
	}
	return out, nil
}
func guidedBottleFixture(t *testing.T, format string) (InstallRequest, *guidedBottleRunner) {
	t.Helper()
	r, in, _, _ := windowsRequest(t)
	data, e := os.ReadFile(in.ExecutablePath)
	if e != nil {
		t.Fatal(e)
	}
	r.Recipe.Entry = "ZaH installer"
	r.Material = &InstallMaterial{Format: format, Mode: "installer", Entry: "program.exe"}
	material := filepath.Join(t.TempDir(), "material")
	if format == "zip" {
		r.Material.Entry = "folder/app.exe"
		file, e := os.Create(material)
		if e != nil {
			t.Fatal(e)
		}
		z := zip.NewWriter(file)
		for name, value := range map[string][]byte{"folder/app.exe": data, "folder/lib.dll": []byte("dependency")} {
			w, _ := z.Create(name)
			_, _ = w.Write(value)
		}
		_ = z.Close()
		_ = file.Close()
		data, e = os.ReadFile(material)
		if e != nil {
			t.Fatal(e)
		}
	} else {
		if e = os.WriteFile(material, data, 0600); e != nil {
			t.Fatal(e)
		}
	}
	digest := sha256.Sum256(data)
	r.ArtifactPath = material
	r.ArtifactSHA256 = hex.EncodeToString(digest[:])
	root := filepath.Join(t.TempDir(), "bottles")
	if e = os.Mkdir(root, 0700); e != nil {
		t.Fatal(e)
	}
	fontDir := filepath.Join(filepath.Dir(root), "runners", r.Recipe.Runner, "share", "wine", "fonts")
	if e = os.MkdirAll(fontDir, 0700); e != nil {
		t.Fatal(e)
	}
	for _, font := range []string{"tahoma.ttf", "tahomabd.ttf"} {
		if e = os.WriteFile(filepath.Join(fontDir, font), []byte("fixture font"), 0600); e != nil {
			t.Fatal(e)
		}
	}
	return r, &guidedBottleRunner{r: r, root: root, programs: map[string]string{}}
}
func TestGuidedBottlePreparePreservesZIPAndNeverStartsInstaller(t *testing.T) {
	for _, format := range []string{"exe", "zip"} {
		t.Run(format, func(t *testing.T) {
			r, f := guidedBottleFixture(t, format)
			if format == "exe" {
				r.Material.Entry = "Original installer.exe"
				r.Material.DPI = 192
				r.Material.HideWineTray = true
				f.r = r
			}
			a := Adapter{Runner: f}
			plan, e := a.PlanInstall(r)
			if e != nil || len(plan.Steps) != 1 || plan.Steps[0].Kind != "prepare_bottle" {
				t.Fatal(plan, e)
			}
			proof, e := a.ExecuteStep(context.Background(), plan.Steps[0])
			if e != nil || proof.Windows == nil || !proof.Windows.ControlledLaunch {
				t.Fatalf("%+v %v", proof, e)
			}
			if format == "exe" {
				if _, e := os.Stat(filepath.Join(proof.Windows.Bottle.Path, "drive_c", "zah-material", "Original installer.exe")); e != nil {
					t.Fatal(e)
				}
			}
			for _, fontFile := range []string{"NotoSansCJK-Regular.ttc", "DejaVuSans.ttf", "ZaH-Noto-OFL.txt", "ZaH-DejaVu-LICENSE.txt"} {
				if _, e := os.Stat(filepath.Join(proof.Windows.Bottle.Path, "drive_c", "windows", "Fonts", fontFile)); e != nil {
					t.Fatal(e)
				}
			}
			var simsun, broaderFallback, hiddenTray, qqMusicOverride bool
			for _, c := range f.calls {
				args := strings.Join(c.Args, "|")
				hiddenTray = hiddenTray || strings.Contains(args, `\Explorer|-v|ShowSystray|-d|0|-t|REG_DWORD`)
				if strings.Contains(args, "SMTCFeature") {
					if !strings.Contains(args, `|-k|HKEY_CURRENT_USER\Software\Wine\AppDefaults\QQMusic.exe\DllOverrides|-v|SMTCFeature|-d||-t|REG_SZ`) {
						t.Fatal("media-control workaround must be an empty QQMusic.exe-only override")
					}
					qqMusicOverride = true
				}
				if strings.Contains(args, `\FontSubstitutes|`) {
					if strings.Contains(args, "|-v|Arial|") || strings.Contains(args, "|-v|Tahoma|") || strings.Contains(args, "|-v|QQMusicUI|") {
						t.Fatal("overrode native Latin or private icon font")
					}
					simsun = simsun || strings.Contains(args, "|-v|SimSun|-d|Noto Sans CJK SC|")
				}
				broaderFallback = broaderFallback || strings.Contains(args, `\FontLink\SystemLink|-v|Noto Sans CJK SC|-d|DejaVuSans.ttf,DejaVu Sans|-t|REG_MULTI_SZ`)
			}
			if !qqMusicOverride {
				t.Fatal("missing verified QQ Music exit workaround")
			}
			if hiddenTray != r.Material.HideWineTray {
				t.Fatal("tray must only be hidden when explicitly selected")
			}
			if !simsun || !broaderFallback {
				t.Fatal("missing the real-QA-verified CJK/broader fallback")
			}
			if format == "zip" {
				data, e := os.ReadFile(filepath.Join(proof.Windows.Bottle.Path, "drive_c", "zah-material", "folder", "lib.dll"))
				if e != nil || string(data) != "dependency" {
					t.Fatal(string(data), e)
				}
			}
			for _, c := range f.calls {
				if len(c.Args) > 3 && c.Args[3] == "run" {
					t.Fatal("installer executed during preparation")
				}
			}
			before := len(f.calls)
			if _, e = a.prepareBottles(context.Background(), r); e == nil {
				t.Fatal("adopted existing Bottle")
			}
			for _, c := range f.calls[before:] {
				if len(c.Args) > 3 && c.Args[3] != "--json" {
					t.Fatal("mutated existing Bottle")
				}
			}
		})
	}
}
func TestSaveProgramRequiresStoppedPrefixAndReviewedDigest(t *testing.T) {
	r, f := guidedBottleFixture(t, "exe")
	a := Adapter{Runner: f}
	proof, e := a.prepareBottles(context.Background(), r)
	if e != nil {
		t.Fatal(e)
	}
	r.Windows = proof.Windows
	installed := filepath.Join(proof.Windows.Bottle.Path, "drive_c", "installed.exe")
	data, _ := os.ReadFile(r.ArtifactPath)
	if e = os.WriteFile(installed, data, 0600); e != nil {
		t.Fatal(e)
	}
	digest, e := a.EntryDigest(context.Background(), r, installed)
	if e != nil {
		t.Fatal(e)
	}
	f.failWait = true
	if _, e = a.SaveProgram(context.Background(), r, proof.ResourceID, "ZaH app-test", installed, digest); e == nil {
		t.Fatal("changed entry with live prefix")
	}
	f.failWait = false
	if _, e = a.SaveProgram(context.Background(), r, proof.ResourceID, "ZaH app-test", installed, strings.Repeat("0", 64)); e == nil {
		t.Fatal("accepted changed entry")
	}
	saved, e := a.SaveProgram(context.Background(), r, proof.ResourceID, "ZaH app-test", installed, digest)
	if e != nil || saved.Entry != "ZaH app-test" {
		t.Fatal(saved, e)
	}
	if _, e = a.EntryDigest(context.Background(), r, r.ArtifactPath); e == nil {
		t.Fatal("accepted external EXE")
	}
}
