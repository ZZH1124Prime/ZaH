package host

import (
	"errors"
	"testing"
)

func TestInspect(t *testing.T) {
	calls := 0
	lookup := func(name string) (string, error) {
		calls++
		if name == "flatpak" {
			return "", errors.New("模拟未安装")
		}
		return "/mock/bin/" + name, nil
	}

	report := inspect(lookup)

	if report.OS == "" || report.Arch == "" {
		t.Fatal("系统或架构为空")
	}
	if calls != 3 || len(report.Tools) != 3 {
		t.Fatalf("检测数量错误: calls=%d, tools=%d", calls, len(report.Tools))
	}

	for i, name := range []string{"docker", "distrobox", "flatpak"} {
		tool := report.Tools[i]
		if tool.Name != name {
			t.Errorf("工具名称错误: got %q, want %q", tool.Name, name)
		}
		if name == "flatpak" {
			if tool.Available || tool.Path != "" || tool.Detail != "模拟未安装" {
				t.Errorf("缺失工具结果错误: %+v", tool)
			}
		} else {
			if !tool.Available || tool.Path != "/mock/bin/"+name || tool.Detail != "" {
				t.Errorf("已找到工具结果错误: %+v", tool)
			}
		}
	}
}
