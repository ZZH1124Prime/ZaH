// Package dependencies plans a deliberately narrow host package transaction.
// Planning is read-only; only Execute after explicit confirmation may elevate.
package dependencies

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/ZZH1124Prime/ZaH/internal/process"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type Plan struct {
	Platform     string   `json:"platform"`
	Distribution string   `json:"distribution"`
	Packages     []string `json:"packages"`
	Fingerprint  string   `json:"fingerprint"`
	Notes        []string `json:"notes"`
	Preparation  string   `json:"preparation,omitempty"`
}
type Installer struct {
	Runner process.Runner
	// Test-only injections. Not exposed over RPC.
	ReadOS          func() ([]byte, error)
	LookPath        func(string) (string, error)
	CheckExecutable func(string) error
}

func (i Installer) runner() process.Runner {
	if i.Runner != nil {
		return i.Runner
	}
	return process.OSRunner{}
}
func (i Installer) lookup(n string) (string, error) {
	if i.LookPath != nil {
		return i.LookPath(n)
	}
	return exec.LookPath(n)
}
func (i Installer) check(p string) error {
	if i.CheckExecutable != nil {
		return i.CheckExecutable(p)
	}
	st, e := os.Stat(p)
	if e != nil {
		return errors.New("缺少系统 apt-get 或 pkexec，请先通过系统软件管理器安装")
	}
	stat, ok := st.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 || st.Mode().Perm()&0022 != 0 || !st.Mode().IsRegular() || st.Mode().Perm()&0111 == 0 {
		return errors.New("系统安装工具权限不符合要求")
	}
	return nil
}
func distribution(raw []byte) (string, error) {
	if len(raw) > 65536 {
		return "", errors.New("发行版信息过大")
	}
	fields := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok && (k == "ID" || k == "VERSION_ID") {
			if _, exists := fields[k]; exists {
				return "", errors.New("发行版信息重复")
			}
			fields[k] = strings.Trim(v, "\"'")
		}
	}
	id := fields["ID"] + " " + fields["VERSION_ID"]
	switch id {
	case "ubuntu 20.04", "ubuntu 22.04", "ubuntu 24.04", "debian 12", "debian 13":
		return id, nil
	}
	return "", errors.New("当前发行版暂不支持自动安装依赖；请使用系统软件管理器，ZaH 不会猜测命令")
}

// Resolve only the helper shipped next to this backend; never accept an RPC path.
func focalPreparation() (string, []byte, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", nil, err
	}
	script := filepath.Join(filepath.Dir(executable), "..", "scripts", "prepare-windows.py")
	data, err := os.ReadFile(script)
	return script, data, err
}

var installLine = regexp.MustCompile(`^Inst ([a-z0-9][a-z0-9+.-]*(?::[a-z0-9]+)?) \(([a-zA-Z0-9.+:~_-]+) `)

func parseSimulation(out process.Result) ([]string, error) {
	if out.Truncated {
		return nil, errors.New("依赖计划输出超出限制")
	}
	packages := []string{}
	seen := map[string]bool{}
	for _, line := range strings.Split(out.Stdout, "\n") {
		if strings.HasPrefix(line, "Remv ") || strings.HasPrefix(line, "Purg ") {
			return nil, errors.New("依赖安装需要删除现有软件，已拒绝自动执行")
		}
		if !strings.HasPrefix(line, "Inst ") {
			continue
		}
		m := installLine.FindStringSubmatch(line)
		if m == nil || strings.Contains(line, " [") && strings.Index(line, " [") < strings.Index(line, " (") {
			return nil, errors.New("依赖安装需要升级现有软件或计划无法识别，已拒绝自动执行")
		}
		if seen[m[1]] {
			return nil, errors.New("重复的软件包记录")
		}
		seen[m[1]] = true
		packages = append(packages, m[1]+"="+m[2])
		if len(packages) > 256 {
			return nil, errors.New("依赖计划过大")
		}
	}
	for _, line := range strings.Split(out.Stdout, "\n") {
		if strings.HasPrefix(line, "Conf ") {
			fields := strings.Fields(line)
			if len(fields) < 2 || !seen[fields[1]] {
				return nil, errors.New("系统存在未完成的包配置事务，请先使用系统软件管理器处理")
			}
		}
	}
	sort.Strings(packages)
	return packages, nil
}
func aptArgs(packages []string, simulate bool) []string {
	args := []string{"--no-remove", "--no-upgrade", "--no-install-recommends", "-o", "Dpkg::Use-Pty=0", "-o", "APT::Color=0"}
	if simulate {
		args = append(args, "--simulate")
	} else {
		args = append(args, "--assume-yes")
	}
	return append(append(args, "install", "--"), packages...)
}
func (i Installer) Plan(ctx context.Context, platform string) (Plan, error) {
	p := Plan{Platform: platform}
	if platform != "linux" && platform != "windows" {
		return p, errors.New("请选择 Linux 或 Windows")
	}
	read := i.ReadOS
	if read == nil {
		read = func() ([]byte, error) { return os.ReadFile("/etc/os-release") }
	}
	raw, e := read()
	if e != nil {
		return p, e
	}
	p.Distribution, e = distribution(raw)
	if e != nil {
		return p, e
	}
	if platform == "windows" && p.Distribution == "ubuntu 20.04" {
		if _, helper, err := focalPreparation(); err == nil {
			p.Preparation = "focal-private"
			p.Packages = []string{"zah-private-flatpak=1.18.4", "bottles=67.5", "soda=11.0-10"}
			p.Notes = []string{"使用现有私有运行时准备命令；已有环境仅检查，不覆盖。", "首次准备需要联网下载并本地编译，可能耗时较长。缺少系统依赖或下载失败时会停止，在日志给出错误及终端命令；不会修改软件源、代理或自动升级宿主。", "基础检查不代表全部 Windows 应用兼容；创建环境时 Bottles 仍可能下载其他组件。完成后请正常关闭并重新打开 ZaH。"}
			sum := sha256.Sum256(append(raw, helper...))
			p.Fingerprint = hex.EncodeToString(sum[:])
			return p, nil
		}
	}
	if e = i.check("/usr/bin/apt-get"); e != nil {
		return p, e
	}
	if e = i.check("/usr/bin/pkexec"); e != nil {
		return p, e
	}
	wanted := []string{}
	commands := []struct{ command, pkg string }{{"docker", "docker.io"}, {"distrobox", "distrobox"}}
	if platform == "windows" {
		commands = []struct{ command, pkg string }{{"flatpak", "flatpak"}}
	}
	for _, c := range commands {
		if _, e = i.lookup(c.command); e != nil {
			wanted = append(wanted, c.pkg)
		}
	}
	if len(wanted) == 0 {
		return p, errors.New("所选平台的基础命令已存在。服务权限、Bottles／Wine 运行器及软件源需要单独配置；不会重装或修改服务")
	}
	out, e := i.runner().Run(ctx, process.Command{Program: "/usr/bin/apt-get", Args: aptArgs(wanted, true), Env: map[string]string{"LC_ALL": "C"}, SafeCancel: true, Timeout: 30 * time.Second, MaxOutput: 256 << 10})
	if e != nil {
		return p, errors.New("无法生成依赖计划；请检查现有软件源、包索引及系统软件管理器状态，不会自动添加源或更新索引")
	}
	p.Packages, e = parseSimulation(out)
	if e != nil {
		return p, e
	}
	if platform == "windows" {
		for _, pkg := range p.Packages {
			name, version, ok := strings.Cut(pkg, "=")
			if ok && strings.Split(name, ":")[0] == "flatpak" && !supportsBottleFlatpak(version) {
				return p, errors.New("现有软件源的 Flatpak 版本过旧，缺少当前 Bottles 所需的 per-app-dev-shm（Flatpak 1.12 起）；不会安装后冒充可用。Ubuntu 20.04 使用 ZaH 固定私有运行时方案（docs/compatibility/FOCAL_RUNTIME.md）；需完成 Bottles 和 runner 的独立安装验收。本操作不会安装旧 PPA、加源或升级宿主")
			}
		}
	}
	if len(p.Packages) == 0 {
		return p, errors.New("基础软件包已安装但命令不可用，请检查 PATH；不会自动重装")
	}
	p.Notes = []string{"从系统现有配置的软件源下载所列固定版本及依赖；不添加软件源或升级、删除现有软件。", "软件包安装脚本可能启动服务、创建系统用户或改变系统配置，尤其是 Docker。请在系统可接受这些影响时确认。", "ZaH 不修改代理、网络、Docker 服务配置或用户组；安装完成仍需重新检查访问权限。", "不自动安装 Bottles 或 Wine 运行器：需要另行确认其来源。", "安装事务开始后取消请求会等待其自然结束，不能回滚已安装的软件。"}
	data, _ := json.Marshal(struct {
		OS       string
		Packages []string
	}{string(raw), p.Packages})
	sum := sha256.Sum256(data)
	p.Fingerprint = hex.EncodeToString(sum[:])
	return p, nil
}
func (i Installer) Execute(ctx context.Context, p Plan, confirmed bool, output func(string)) (process.Result, error) {
	if !confirmed {
		return process.Result{}, errors.New("尚未确认宿主软件安装及其系统影响")
	}
	if e := ctx.Err(); e != nil {
		return process.Result{}, e
	}
	current, e := i.Plan(ctx, p.Platform)
	if e != nil {
		return process.Result{}, e
	}
	if p.Fingerprint == "" || current.Fingerprint != p.Fingerprint {
		return process.Result{}, errors.New("系统或依赖计划已改变，请重新检查并确认")
	}
	if current.Preparation == "focal-private" {
		script, _, err := focalPreparation()
		if err != nil {
			return process.Result{}, err
		}
		result, err := i.runner().Run(ctx, process.Command{Program: "/usr/bin/python3", Args: []string{"-u", script, "--install"}, SafeCancel: false, MaxOutput: 4096, Output: output})
		result.Stdout, result.Stderr = "", ""
		if err != nil {
			return result, fmt.Errorf("Windows 依赖准备未完成（退出码 %d）；请按安装日志中的错误和终端命令处理，未完成部分不会标为成功", result.ExitCode)
		}
		return result, nil
	}
	// Never trust serialized argv, even after confirmation; regenerate from the fresh plan.
	args := append([]string{"--disable-internal-agent", "/usr/bin/apt-get"}, aptArgs(current.Packages, false)...)
	result, e := i.runner().Run(ctx, process.Command{Program: "/usr/bin/pkexec", Args: args, SafeCancel: false, MaxOutput: 4096, Output: output})
	result.Stdout = ""
	result.Stderr = ""
	if result.ExitCode == 126 {
		return result, errors.New("已取消系统授权，未确认安装完成")
	}
	if result.ExitCode == 127 {
		return result, errors.New("系统未授予安装权限或缺少桌面授权代理；请使用系统软件管理器处理")
	}
	if e != nil {
		return result, fmt.Errorf("安装未完成（退出码 %d）；请查看日志并重新检查后重试，已完成部分会保留", result.ExitCode)
	}
	return result, nil
}

// This is only the documented per-app-dev-shm feature floor, not a complete
// Bottles/runtime compatibility guarantee. Do not accept unrecognized versions.
func supportsBottleFlatpak(version string) bool {
	if _, rest, ok := strings.Cut(version, ":"); ok {
		version = rest
	}
	m := regexp.MustCompile(`^(\d+)\.(\d+)(?:[.~-]|$)`).FindStringSubmatch(version)
	if m == nil {
		return false
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	return major > 1 || major == 1 && minor >= 12
}
