package backend

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/ZZH1124Prime/ZaH/internal/host"
)

type WindowsBindingEvidence struct {
	Drive         string               `json:"drive"`
	Host          host.BindingEvidence `json:"host"`
	LinkDevice    uint64               `json:"link_device"`
	LinkInode     uint64               `json:"link_inode"`
	LinkTarget    string               `json:"link_target"`
	FlatpakAccess string               `json:"flatpak_access"`
	Scope         string               `json:"scope"`
}

func validWindowsDrive(drive string) bool {
	return len(drive) == 2 && drive[0] >= 'D' && drive[0] <= 'Y' && drive[1] == ':'
}

// verifyWindowsBindings observes manual Winecfg mappings and Flatpak's current
// deployment permission context; it never modifies links, overrides or bottles.
// The proof does not claim live old sandboxes or alternate paths are isolated.
func (a Adapter) verifyWindowsBindings(ctx context.Context, r InstallRequest, bottle host.BindingEvidence) ([]WindowsBindingEvidence, error) {
	if len(r.Bindings) == 0 {
		return nil, nil
	}
	if err := host.VerifyBinding(bottle); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(bottle.Path)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	info, err := root.Lstat("dosdevices")
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: configure the selected bottle's Wine drives in Winecfg first", ErrManual)
	}
	proof := make([]WindowsBindingEvidence, 0, len(r.Bindings))
	for _, binding := range r.Bindings {
		if !validWindowsDrive(binding.Target) {
			return nil, fmt.Errorf("%w: Windows drive must be D: through Y:", ErrManual)
		}
		if err := host.VerifyBinding(binding.Host); err != nil {
			return nil, err
		}
		member := "dosdevices/" + strings.ToLower(binding.Target)
		before, err := root.Lstat(member)
		if err != nil || before.Mode()&os.ModeSymlink == 0 {
			return nil, fmt.Errorf("%w: Wine drive %s is not a configured directory link", ErrManual, binding.Target)
		}
		stat, ok := before.Sys().(*syscall.Stat_t)
		if !ok {
			return nil, fmt.Errorf("%w: Wine drive identity unavailable", ErrManual)
		}
		target, err := root.Readlink(member)
		if err != nil {
			return nil, err
		}
		if strings.ContainsAny(target, "\x00\r\n") {
			return nil, fmt.Errorf("%w: invalid Wine drive target", ErrManual)
		}
		resolved := target
		if !filepath.IsAbs(resolved) {
			resolved = filepath.Join(bottle.Path, "dosdevices", resolved)
		}
		resolved, err = filepath.EvalSymlinks(resolved)
		if err != nil || resolved != binding.Host.Path {
			return nil, fmt.Errorf("%w: Wine drive %s does not resolve to the selected host directory", ErrManual, binding.Target)
		}
		result, err := a.runner().Run(ctx, command("flatpak", "info", "--file-access="+binding.Host.Path, "com.usebottles.bottles"))
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, fmt.Errorf("%w: cannot verify effective Bottles Flatpak path permission", ErrManual)
		}
		access := strings.TrimSpace(result.Stdout)
		want := "read-write"
		if binding.Host.ReadOnly {
			want = "read-only"
		}
		if result.Truncated || access != want {
			return nil, fmt.Errorf("%w: drive %s requires exact %s Flatpak path access; configure permissions manually and close old application processes", ErrManual, binding.Target, want)
		}
		after, err := root.Lstat(member)
		if err != nil || !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
			return nil, fmt.Errorf("%w: Wine drive changed during verification", ErrManual)
		}
		next, err := root.Readlink(member)
		if err != nil || next != target {
			return nil, fmt.Errorf("%w: Wine drive target changed during verification", ErrManual)
		}
		if err = host.VerifyBinding(binding.Host); err != nil {
			return nil, err
		}
		proof = append(proof, WindowsBindingEvidence{Drive: binding.Target, Host: binding.Host, LinkDevice: uint64(stat.Dev), LinkInode: stat.Ino, LinkTarget: target, FlatpakAccess: access, Scope: "configured_path_access_not_live_sandbox_or_app_capability"})
	}
	sort.Slice(proof, func(i, j int) bool { return proof[i].Drive < proof[j].Drive })
	return proof, nil
}
