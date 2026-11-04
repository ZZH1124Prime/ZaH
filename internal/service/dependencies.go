package service

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/ZZH1124Prime/ZaH/internal/dependencies"
	"github.com/ZZH1124Prime/ZaH/internal/domain"
	"github.com/ZZH1124Prime/ZaH/internal/jobs"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
)

func (s *Service) planDependencies(ctx context.Context, req planRequest) (domain.Plan, error) {
	var input struct {
		Platform string `json:"platform"`
	}
	p := domain.Plan{ID: domain.NewID(), Operation: req.Operation, CreatedAt: time.Now().UTC()}
	if e := decode(req.Input, &input); e != nil {
		return p, e
	}
	details, e := s.Dependencies.Plan(ctx, input.Platform)
	if e != nil {
		return p, e
	}
	p.Input, _ = json.Marshal(details)
	p.DataImpact = append([]string(nil), details.Notes...)
	p.Permissions = []string{"需要原生确认对话框及系统管理员授权；可能启动新安装软件的服务"}
	p.Preconditions = []string{"重新检查发行版及固定包版本；拒绝删除或升级现有软件"}
	p.Steps = []domain.Step{{ID: domain.NewID(), Kind: p.Operation, Description: "安装已确认的宿主依赖", Params: p.Input, SafeCancel: false}}
	return p, s.save(ctx, "plan", p.ID, p)
}

type dependencyProgress struct {
	Lines     []string `json:"lines"`
	Phase     string   `json:"phase"`
	Truncated bool     `json:"truncated"`
}

var logURL = regexp.MustCompile(`(?i)(?:[a-z][a-z0-9+.-]*://|www\.)\S+`)

func cleanDependencyLine(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	lower := strings.ToLower(s)
	for _, word := range []string{"password", "passwd", "token", "secret", "authorization", "credential"} {
		if strings.Contains(lower, word) {
			return "[含敏感字段的日志已隐藏]"
		}
	}
	s = logURL.ReplaceAllString(s, "[地址已隐藏]")
	if len(s) > 2048 {
		s = s[:2048] + "…"
	}
	return s
}
func (s *Service) executeDependencies(ctx context.Context, p domain.Plan) (any, error) {
	id := jobs.ID(ctx)
	j, e := get[domain.Job](ctx, s.Store, "job", id)
	if e != nil || id == "" || j.PlanID != p.ID || !j.HostChangesConfirmed {
		return nil, errors.New("宿主安装必须经过明确确认的持久任务执行")
	}
	var details dependencies.Plan
	if e = decode(p.Input, &details); e != nil {
		return nil, e
	}
	progress := dependencyProgress{Lines: []string{}, Phase: "重新验证依赖计划"}
	var mu sync.Mutex
	pending := ""
	dropping := false
	last := time.Time{}
	publish := func(force bool) {
		if force || time.Since(last) >= 250*time.Millisecond {
			data, _ := json.Marshal(progress)
			_ = s.Store.SetJobResult(context.WithoutCancel(ctx), id, data)
			last = time.Now()
		}
	}
	appendLine := func(line string) {
		if len(progress.Lines) >= 160 {
			progress.Lines = progress.Lines[1:]
			progress.Truncated = true
		}
		progress.Lines = append(progress.Lines, cleanDependencyLine(line))
	}
	publish(true)
	_, e = s.Dependencies.Execute(ctx, details, j.HostChangesConfirmed, func(chunk string) {
		mu.Lock()
		defer mu.Unlock()
		progress.Phase = "等待系统授权或安装中"
		if dropping {
			_, rest, ok := strings.Cut(chunk, "\n")
			if !ok {
				return
			}
			chunk = rest
			dropping = false
		}
		pending += chunk
		for {
			line, rest, ok := strings.Cut(pending, "\n")
			if !ok {
				break
			}
			appendLine(line)
			pending = rest
		}
		if len(pending) > 4096 {
			pending = ""
			dropping = true
			appendLine("[超长日志行已隐藏]")
		}
		publish(false)
	})
	mu.Lock()
	defer mu.Unlock()
	if pending != "" {
		appendLine(pending)
	}
	if e != nil {
		progress.Phase = "安装未完成"
	} else {
		progress.Phase = "安装事务已完成，请重新检查依赖"
	}
	publish(true)
	return progress, e
}
