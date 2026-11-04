package host

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

type RuntimeCheck struct {
	Name      string `json:"name"`
	Available bool   `json:"available"`
	Detail    string `json:"detail"`
}
type RuntimeReport struct {
	Report
	Checks []RuntimeCheck `json:"checks"`
}

// InspectRuntime probes existing services read-only. It never launches a Bottle,
// pulls images, installs packages, changes permissions or starts a daemon.
func InspectRuntime(ctx context.Context) RuntimeReport {
	return inspectRuntime(ctx, Inspect(), func(ctx context.Context, program string, args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, program, args...)
		cmd.Stdout = io.Discard
		cmd.Stderr = io.Discard
		return "", cmd.Run()
	})
}
func inspectRuntime(ctx context.Context, report Report, run func(context.Context, string, ...string) (string, error)) RuntimeReport {
	result := RuntimeReport{Report: report, Checks: []RuntimeCheck{}}
	available := map[string]bool{}
	for _, t := range report.Tools {
		available[t.Name] = t.Available
	}
	probes := []struct {
		name, program string
		args          []string
		missing       string
	}{
		{"Docker 服务与当前用户权限", "docker", []string{"info", "--format", "{{.ServerVersion}}"}, "未找到 Docker；Linux 应用暂不可安装或运行。"},
		{"Distrobox 命令可执行", "distrobox", []string{"--version"}, "未找到 Distrobox；Linux 应用暂不可安装或运行。"},
		{"Flatpak 版 Bottles", "flatpak", []string{"info", "com.usebottles.bottles"}, "未找到 Flatpak；Windows 应用暂不可安装或运行。"},
	}
	for _, p := range probes {
		check := RuntimeCheck{Name: p.name}
		if !available[p.program] {
			check.Detail = p.missing
			result.Checks = append(result.Checks, check)
			continue
		}
		probe, cancel := context.WithTimeout(ctx, 5*time.Second)
		_, err := run(probe, p.program, p.args...)
		timedOut := probe.Err()
		cancel()
		if err == nil {
			check.Available = true
			check.Detail = "检查通过；不代表任意第三方应用兼容。"
		} else if timedOut != nil {
			check.Detail = "检查超时或已取消，请检查本机依赖状态后重试。"
		} else {
			check.Detail = fmt.Sprintf("检查未通过（%s）。未修改服务或权限，请在系统中检查该依赖。", strings.Join(append([]string{p.program}, p.args...), " "))
		}
		result.Checks = append(result.Checks, check)
	}
	return result
}
