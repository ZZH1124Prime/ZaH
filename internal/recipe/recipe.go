// Package recipe validates versioned declarative application recipes. Recipes
// never supply shell commands or host environment variables.
package recipe

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"path"
	"regexp"
	"strings"
)

const MaxBytes = 256 << 10

// Recipe fixes the installation strategy and the installed entry to verify.
type LinuxEntry struct {
	Kind         string `json:"kind"`
	Path         string `json:"path"`
	CWD          string `json:"cwd,omitempty"`
	SHA256       string `json:"sha256"`
	HomeRelative bool   `json:"home_relative,omitempty"`
}

type Recipe struct {
	PendingEntry         bool        `json:"pending_entry,omitempty"`
	LinuxEntry           *LinuxEntry `json:"linux_entry,omitempty"`
	SchemaVersion        int         `json:"schema_version"`
	ID                   string      `json:"id"`
	Revision             string      `json:"revision"`
	Platform             string      `json:"platform"`
	Architecture         string      `json:"architecture"`
	Strategy             string      `json:"strategy"`
	Image                string      `json:"image,omitempty"`
	Runner               string      `json:"runner,omitempty"`
	Dependencies         []string    `json:"dependencies,omitempty"`
	Entry                string      `json:"entry"`
	ConflictGroups       []string    `json:"conflict_groups,omitempty"`
	Lifecycle            string      `json:"lifecycle,omitempty"`
	RequiredCapabilities []string    `json:"required_capabilities,omitempty"`
}

var token = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.+-]{0,127}$`)
var image = regexp.MustCompile(`^[a-z0-9][a-z0-9._/:+-]*@sha256:[a-f0-9]{64}$`)

// Decode accepts exactly one bounded JSON object with known fields.
func Decode(r io.Reader) (Recipe, error) {
	b, err := io.ReadAll(io.LimitReader(r, MaxBytes+1))
	if err != nil {
		return Recipe{}, err
	}
	if len(b) > MaxBytes {
		return Recipe{}, errors.New("recipe exceeds size limit")
	}
	var v Recipe
	d := json.NewDecoder(strings.NewReader(string(b)))
	d.DisallowUnknownFields()
	if err = d.Decode(&v); err != nil {
		return v, err
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return v, errors.New("trailing recipe content")
	}
	return v, v.Validate()
}

// Validate rejects dynamic tags, shell templates and incompatible strategies.
func (v Recipe) Validate() error {
	if v.SchemaVersion != 1 || !token.MatchString(v.ID) || !token.MatchString(v.Revision) {
		return errors.New("invalid recipe identity or schema")
	}
	if v.Lifecycle != "" && (v.Lifecycle != "process-tree" || v.Platform != "linux") {
		return errors.New("only verified Linux process-tree lifecycle is supported")
	}
	if v.Architecture != "amd64" {
		return errors.New("only amd64 recipes supported")
	}
	if len(v.Dependencies) > 128 || len(v.ConflictGroups) > 32 || len(v.RequiredCapabilities) > 32 {
		return errors.New("recipe collection exceeds limit")
	}
	for _, list := range [][]string{v.Dependencies, v.ConflictGroups, v.RequiredCapabilities} {
		for _, s := range list {
			if !token.MatchString(s) {
				return errors.New("invalid recipe token")
			}
		}
	}
	if len(v.Entry) > 4096 || strings.ContainsAny(v.Entry, "\x00\r\n") || (v.Entry == "" && !v.PendingEntry) {
		return errors.New("invalid entry")
	}
	for _, c := range v.RequiredCapabilities {
		switch c {
		case "display", "audio", "files":
		default:
			return errors.New("capability requires an unimplemented specialized adapter")
		}
	}
	if (v.PendingEntry || v.LinuxEntry != nil) && v.Platform != "linux" {
		return errors.New("Linux entry metadata on another platform")
	}
	if v.PendingEntry && v.LinuxEntry != nil {
		return errors.New("pending entry cannot also be selected")
	}
	if selected := v.LinuxEntry; selected != nil {
		if (selected.Kind != "desktop" && selected.Kind != "executable") || len(selected.SHA256) != 64 || len(selected.Path) > 4096 || strings.ContainsAny(selected.Path+selected.CWD, "\x00\r\n") || path.Clean(selected.Path) != selected.Path {
			return errors.New("invalid selected Linux entry")
		}
		if _, e := hex.DecodeString(selected.SHA256); e != nil {
			return e
		}
		if selected.HomeRelative {
			if path.IsAbs(selected.Path) || selected.Path == ".." || strings.HasPrefix(selected.Path, "../") {
				return errors.New("invalid private Linux entry")
			}
		} else if !path.IsAbs(selected.Path) {
			return errors.New("absolute Linux entry required")
		}
	}
	switch v.Platform {
	case "linux":
		if !image.MatchString(v.Image) || v.Runner != "" {
			return errors.New("linux image must be pinned by sha256 digest")
		}
		switch v.Strategy {
		case "appimage":
			if v.Entry != "squashfs-root/AppRun" {
				return errors.New("AppImage entry must be squashfs-root/AppRun")
			}
		case "deb":
			if !v.PendingEntry && v.LinuxEntry == nil && (!path.IsAbs(v.Entry) || path.Clean(v.Entry) != v.Entry || !strings.HasPrefix(v.Entry, "/usr/")) {
				return errors.New("deb entry must be a normalized /usr path")
			}
		default:
			return errors.New("unsupported Linux strategy")
		}
	case "windows":
		if v.Image != "" || len(v.Dependencies) != 0 || !token.MatchString(v.Runner) {
			return errors.New("Windows recipe requires explicit runner and no Linux dependencies")
		}
		if v.Strategy != "exe" && v.Strategy != "msi" {
			return errors.New("unsupported Windows strategy")
		}
		if strings.HasPrefix(v.Entry, "-") {
			return errors.New("program name cannot start with option prefix")
		}
	default:
		return errors.New("unsupported platform")
	}
	return nil
}

// Digest identifies the entire normalized recipe rather than trusting its label.
func (v Recipe) Digest() string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
