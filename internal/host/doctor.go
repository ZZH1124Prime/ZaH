package host

import (
	"os/exec"
	"runtime"
)

type ToolCheck struct {
	Name      string
	Path      string
	Available bool
	Detail    string
}

type Report struct {
	OS    string
	Arch  string
	Tools []ToolCheck
}

func Inspect() Report {
	return inspect(exec.LookPath)
}

func inspect(lookup func(string) (string, error)) Report {
	report := Report{
		OS:   runtime.GOOS,
		Arch: runtime.GOARCH,
	}

	for _, name := range []string{"docker", "distrobox", "flatpak"} {
		path, err := lookup(name)
		check := ToolCheck{Name: name}
		if err != nil {
			check.Detail = err.Error()
		} else {
			check.Path = path
			check.Available = true
		}
		report.Tools = append(report.Tools, check)
	}
	return report
}
