package backend

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
)

type InstallOption struct {
	Value   string `json:"value"`
	Label   string `json:"label"`
	Default bool   `json:"default,omitempty"`
}

// InstallOptions only lists locally available bases. It never pulls an image,
// installs a runner or changes Flatpak permissions. Invocation is user-initiated.
func (a Adapter) InstallOptions(ctx context.Context, platform string) ([]InstallOption, error) {
	options := []InstallOption{}
	if platform == "linux" {
		result, e := a.runner().Run(ctx, command("docker", "image", "ls", "--digests", "--no-trunc", "--format", "{{json .}}"))
		if e != nil {
			return nil, e
		}
		if result.Truncated {
			return nil, errors.New("image inventory exceeds limit")
		}
		seen := map[string]bool{}
		scan := bufio.NewScanner(strings.NewReader(result.Stdout))
		for scan.Scan() {
			var row struct{ Repository, Tag, Digest string }
			if e = json.Unmarshal(scan.Bytes(), &row); e != nil {
				return nil, e
			}
			// The automatic deb guide is deliberately scoped to supported Ubuntu bases.
			if row.Repository != "ubuntu" && row.Repository != "docker.io/library/ubuntu" {
				continue
			}
			if row.Tag != "22.04" && row.Tag != "24.04" && row.Tag != "26.04" {
				continue
			}
			if !strings.HasPrefix(row.Digest, "sha256:") || !digestPattern.MatchString(strings.TrimPrefix(row.Digest, "sha256:")) {
				continue
			}
			value := row.Repository + "@" + row.Digest
			if seen[value] {
				continue
			}
			seen[value] = true
			options = append(options, InstallOption{Value: value, Label: "Ubuntu " + row.Tag + " · 本地镜像", Default: row.Tag == "26.04"})
		}
		if e = scan.Err(); e != nil {
			return nil, e
		}
	} else if platform == "windows" {
		result, e := a.runner().Run(ctx, bottles("--json", "list", "components", "-f", "category:runners"))
		if e != nil {
			return nil, e
		}
		if result.Truncated {
			return nil, errors.New("runner inventory exceeds limit")
		}
		var resultJSON struct {
			Runners []string `json:"runners"`
		}
		if e = json.Unmarshal([]byte(result.Stdout), &resultJSON); e != nil {
			return nil, errors.New("unsupported Bottles runner inventory")
		}
		for _, runner := range resultJSON.Runners {
			if len(runner) > 128 || strings.ContainsAny(runner, "/\\\x00\r\n") || strings.HasPrefix(runner, "sys-") || strings.Contains(strings.ToLower(runner), "proton") {
				continue
			}
			options = append(options, InstallOption{Value: runner, Label: runner})
		}
	} else {
		return nil, ErrUnsupported
	}
	if len(options) > 256 {
		return nil, errors.New("too many installation options")
	}
	sort.Slice(options, func(i, j int) bool { return options[i].Label < options[j].Label })
	return options, nil
}
