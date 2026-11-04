package backend

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strconv"
)

// The unmodified OFL font and its license ship with the program. No font is
// downloaded during installation and no host font or display setting is changed.
//
//go:embed fonts/NotoSansCJK-Regular.ttc
var windowsCJKFont []byte

//go:embed fonts/LICENSE.txt
var windowsCJKLicense []byte

//go:embed fonts/DejaVuSans.ttf
var windowsFallbackFont []byte

//go:embed fonts/DejaVu-LICENSE.txt
var windowsFallbackLicense []byte

func (a Adapter) prepareWindowsDisplay(ctx context.Context, r InstallRequest, root string) error {
	windows := filepath.Join(root, "drive_c", "windows")
	fonts := filepath.Join(windows, "Fonts")
	for _, dir := range []string{windows, fonts} {
		if e := os.Mkdir(dir, 0700); e != nil && !os.IsExist(e) {
			return e
		}
		if resolved, e := filepath.EvalSymlinks(dir); e != nil || resolved != dir {
			return errors.New("Bottle font directory traverses links")
		}
	}
	for name, data := range map[string][]byte{
		"NotoSansCJK-Regular.ttc": windowsCJKFont, "ZaH-Noto-OFL.txt": windowsCJKLicense,
		"DejaVuSans.ttf": windowsFallbackFont, "ZaH-DejaVu-LICENSE.txt": windowsFallbackLicense,
	} {
		target := filepath.Join(fonts, name)
		f, e := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if os.IsExist(e) {
			digest := sha256.Sum256(data)
			if e = verifyFile(ctx, target, hex.EncodeToString(digest[:])); e != nil {
				return e
			}
			continue
		}
		if e != nil {
			return e
		}
		_, e = f.Write(data)
		if e == nil {
			e = f.Sync()
		}
		closed := f.Close()
		if e != nil {
			return e
		}
		if closed != nil {
			return closed
		}
	}
	// Bottles can register bare Tahoma filenames without copying the runner
	// fonts. Chromium then recurses between Tahoma and sans until stack overflow.
	if !componentName.MatchString(r.Recipe.Runner) {
		return errors.New("invalid font runner")
	}
	for _, font := range []string{"tahoma.ttf", "tahomabd.ttf"} {
		target := filepath.Join(fonts, font)
		if info, e := os.Lstat(target); e == nil {
			if !info.Mode().IsRegular() || info.Size() == 0 {
				return errors.New("invalid existing Tahoma font")
			}
			continue // Preserve installed/application-provided fonts.
		} else if !os.IsNotExist(e) {
			return e
		}
		source := filepath.Join(filepath.Dir(filepath.Dir(root)), "runners", r.Recipe.Runner, "share", "wine", "fonts", font)
		if resolved, e := filepath.EvalSymlinks(source); e != nil || resolved != source {
			return errors.New("runner font missing or traverses links")
		}
		info, e := os.Stat(source)
		if e != nil || !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > 10<<20 {
			return errors.New("invalid runner Tahoma font")
		}
		data, e := os.ReadFile(source)
		if e != nil {
			return e
		}
		f, e := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			return e
		}
		_, e = f.Write(data)
		closed := f.Close()
		if e != nil {
			return e
		}
		if closed != nil {
			return closed
		}
	}
	reg := func(key, value, data, kind string) error {
		c := bottles("reg", "add", "-b", name(r.InstanceID), "-k", key, "-v", value, "-d", data, "-t", kind)
		c.SafeCancel = false
		c.Timeout = 0
		_, e := a.runner().Run(ctx, c)
		return e
	}
	// Flutter device_info_plus opens this key even when MachineId is absent.
	// Wine omits it; create only the empty key, never a fabricated device ID.
	compat := bottles("shell", "-b", name(r.InstanceID), "-i", `reg add "HKLM\SOFTWARE\Microsoft\SQMClient" /f`)
	compat.SafeCancel = false
	compat.Timeout = 0
	if _, e := a.runner().Run(ctx, compat); e != nil {
		return e
	}
	// QQ Music unloads SMTCFeature and later calls it during exit under Wine.
	// Keep this verified workaround executable-scoped; in-app playback remains available.
	if e := reg(`HKEY_CURRENT_USER\Software\Wine\AppDefaults\QQMusic.exe\DllOverrides`, "SMTCFeature", "", "REG_SZ"); e != nil {
		return e
	}
	base := `HKEY_LOCAL_MACHINE\Software\Microsoft\Windows NT\CurrentVersion`
	for _, font := range []struct{ family, file string }{
		{"Tahoma (TrueType)", "tahoma.ttf"},
		{"Tahoma Bold (TrueType)", "tahomabd.ttf"},
	} {
		if e := reg(base+`\Fonts`, font.family, font.file, "REG_SZ"); e != nil {
			return e
		}
	}
	if e := reg(base+`\Fonts`, "Noto Sans CJK SC (TrueType)", "NotoSansCJK-Regular.ttc", "REG_SZ"); e != nil {
		return e
	}
	for _, alias := range []string{"Microsoft YaHei", "微软雅黑", "MS Shell Dlg", "SimSun"} {
		if e := reg(base+`\FontSubstitutes`, alias, "Noto Sans CJK SC", "REG_SZ"); e != nil {
			return e
		}
	}
	// Soda's DirectWrite fallback uses Microsoft YaHei for zh-Hans and
	// MS Gothic for neutral/zh-CN text. FontSubstitutes alone does not
	// expose missing families to DirectWrite; Wine replacements do.
	for _, family := range []struct{ alias, target string }{
		{"Microsoft YaHei", "Noto Sans CJK SC"},
		{"MS Gothic", "Noto Sans CJK JP"},
	} {
		if e := reg(`HKEY_CURRENT_USER\Software\Wine\Fonts\Replacements`, family.alias, family.target, "REG_SZ"); e != nil {
			return e
		}
		if e := reg(base+`\FontSubstitutes`, family.alias, family.target, "REG_SZ"); e != nil {
			return e
		}
	}
	// Retain native Arial/Tahoma and application icon fonts. Their existing
	// CJK fallback can resolve SimSun through the alias above. Link our Noto
	// font to DejaVu for additional glyphs without replacing Latin faces or
	// rewriting Wine's default SystemLink chains (which Wine may regenerate).
	if e := reg(base+`\Fonts`, "DejaVu Sans (TrueType)", "DejaVuSans.ttf", "REG_SZ"); e != nil {
		return e
	}
	if e := reg(base+`\FontLink\SystemLink`, "Noto Sans CJK SC", "DejaVuSans.ttf,DejaVu Sans", "REG_MULTI_SZ"); e != nil {
		return e
	}
	// Opt in per installation: tray-only programs must retain their controls.
	if r.Material.HideWineTray {
		if e := reg(`HKEY_CURRENT_USER\Software\Wine\Explorer`, "ShowSystray", "0", "REG_DWORD"); e != nil {
			return e
		}
	}
	if r.Material.DPI != 0 {
		if e := reg(`HKEY_CURRENT_USER\Control Panel\Desktop`, "LogPixels", strconv.Itoa(r.Material.DPI), "REG_DWORD"); e != nil {
			return e
		}
		c := bottles("edit", "-b", name(r.InstanceID), "--params", "custom_dpi:"+strconv.Itoa(r.Material.DPI))
		c.SafeCancel = false
		c.Timeout = 0
		if _, e := a.runner().Run(ctx, c); e != nil {
			return e
		}
	}
	return nil
}
