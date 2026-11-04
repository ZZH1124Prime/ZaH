// Package bootstrap assembles CLI, manager, and worker roles of one executable.
package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"time"

	"github.com/ZZH1124Prime/ZaH/internal/host"
	"github.com/ZZH1124Prime/ZaH/internal/infra"
	"github.com/ZZH1124Prime/ZaH/internal/process"
	"github.com/ZZH1124Prime/ZaH/internal/transport"
)

// Version is populated by release build flags; source builds report VCS evidence.
var Version = "source"

const helpText = `用法: zah <命令>
  paths                     显示解析后的 XDG 路径（只读）
  version                   显示构建版本
  doctor [--json]            只读检查基础环境，不联系后端服务
  manager                   启动本用户管理服务
  rpc METHOD [JSON|-]        调用管理 API（- 从标准输入读 JSON）
  launch INSTANCE_ID        提交已登记实例启动任务
  help                      显示帮助
长操作使用 plan.create → job.submit；RPC 和数据说明见 docs/GO_CORE.md。
`

func RunCLI(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stdout, helpText)
		return 0
	}
	switch args[0] {
	case "help", "--help", "-h":
		if len(args) != 1 {
			return failure(stderr, fmt.Errorf("help does not accept arguments"))
		}
		fmt.Fprint(stdout, helpText)
		return 0
	case "paths":
		if len(args) != 1 {
			return failure(stderr, fmt.Errorf("paths accepts no arguments"))
		}
		p, e := infra.Resolve()
		if e != nil {
			return failure(stderr, e)
		}
		return encode(stdout, stderr, p)
	case "version":
		if len(args) != 1 {
			return failure(stderr, fmt.Errorf("version does not accept arguments"))
		}
		revision := "unknown"
		modified := "unknown"
		if info, ok := debug.ReadBuildInfo(); ok {
			for _, setting := range info.Settings {
				if setting.Key == "vcs.revision" {
					revision = setting.Value
				}
				if setting.Key == "vcs.modified" {
					modified = setting.Value
				}
			}
		}
		fmt.Fprintf(stdout, "ZaH %s (revision=%s modified=%s)\n", Version, revision, modified)
		return 0
	case "doctor":
		if len(args) > 2 || len(args) == 2 && args[1] != "--json" {
			return failure(stderr, fmt.Errorf("doctor [--json]"))
		}
		r := host.Inspect()
		if len(args) == 2 {
			return encode(stdout, stderr, r)
		}
		fmt.Fprintf(stdout, "系统: %s\n架构: %s\n", r.OS, r.Arch)
		for _, t := range r.Tools {
			if t.Available {
				fmt.Fprintf(stdout, "[找到] %s: %s\n", t.Name, t.Path)
			} else {
				fmt.Fprintf(stdout, "[未找到可用命令] %s: %s\n", t.Name, t.Detail)
			}
		}
		fmt.Fprintln(stdout, "说明: 仅检查命令路径，未检查服务、权限或应用运行状态。")
		return 0
	case "manager":
		return manager(args[1:], stdout, stderr)
	case "worker":
		return worker(args[1:], stderr)
	case "supervise", "supervise-controlled":
		if len(args) < 4 {
			return failure(stderr, fmt.Errorf("supervise NONCE STARTED_PATH ABSOLUTE_ENTRY [ARGS...]"))
		}
		supervise := process.Supervise
		if args[0] == "supervise-controlled" {
			supervise = process.SuperviseControlled
		}
		if err := supervise(args[1], args[2], args[3], args[4:], stdout); err != nil {
			return failure(stderr, err)
		}
		return 0
	case "rpc":
		if len(args) < 2 || len(args) > 3 {
			return failure(stderr, fmt.Errorf("rpc METHOD [JSON|-]"))
		}
		raw := json.RawMessage(`{}`)
		if len(args) == 3 {
			if args[2] == "-" {
				b, e := io.ReadAll(io.LimitReader(os.Stdin, 1<<20+1))
				if e != nil {
					return failure(stderr, e)
				}
				if len(b) > 1<<20 {
					return failure(stderr, fmt.Errorf("input too large"))
				}
				raw = b
			} else {
				raw = []byte(args[2])
			}
		}
		if !json.Valid(raw) {
			return failure(stderr, fmt.Errorf("invalid JSON parameters"))
		}
		var result any
		if e := call(args[1], raw, &result); e != nil {
			return failure(stderr, e)
		}
		return encode(stdout, stderr, result)
	case "launch":
		if len(args) != 2 {
			return failure(stderr, fmt.Errorf("launch INSTANCE_ID"))
		}
		var plan struct {
			ID string `json:"id"`
		}
		if e := call("plan.create", map[string]any{"operation": "instance.launch", "input": map[string]string{"id": args[1]}}, &plan); e != nil {
			return failure(stderr, e)
		}
		var result any
		if e := call("job.submit", map[string]string{"plan_id": plan.ID, "idempotency_key": "launch-" + plan.ID}, &result); e != nil {
			return failure(stderr, e)
		}
		return encode(stdout, stderr, result)
	default:
		return failure(stderr, fmt.Errorf("未知命令: %s", args[0]))
	}
}
func encode(out, errout io.Writer, v any) int {
	e := json.NewEncoder(out)
	e.SetIndent("", "  ")
	if err := e.Encode(v); err != nil {
		return failure(errout, err)
	}
	return 0
}
func call(method string, params any, result any) error {
	paths, e := infra.Resolve()
	if e != nil {
		return e
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if e = ensureManager(ctx, paths); e != nil {
		return e
	}
	return transport.Call(ctx, filepath.Join(paths.Runtime, "manager.sock"), method, params, result)
}
