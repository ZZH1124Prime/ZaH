package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/ZZH1124Prime/ZaH/internal/host"
)

type ImageEvidence struct {
	ID           string `json:"id"`
	Reference    string `json:"reference"`
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
	Size         int64  `json:"size"`
}
type CapacityCheck struct {
	Kind                     string `json:"kind"`
	Path                     string `json:"path"`
	ObservedPath             string `json:"observed_path"`
	Device                   uint64 `json:"device"`
	AvailableBytes           uint64 `json:"available_bytes"`
	EstimatedAdditionalBytes uint64 `json:"estimated_additional_bytes"`
	FilesystemRequiredBytes  uint64 `json:"filesystem_required_bytes"`
	State                    string `json:"state"`
}
type InstallPreflight struct {
	State        string          `json:"state"`
	DockerDriver string          `json:"docker_driver,omitempty"`
	Image        *ImageEvidence  `json:"image,omitempty"`
	Capacity     []CapacityCheck `json:"capacity"`
	Assumptions  []string        `json:"assumptions"`
}

func nearestRealDirectory(path string) (string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", errors.New("absolute normalized capacity path required")
	}
	for candidate := path; ; candidate = filepath.Dir(candidate) {
		st, e := os.Lstat(candidate)
		if e == nil {
			if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
				return "", errors.New("capacity target is not a real directory")
			}
			for p := candidate; p != "/"; p = filepath.Dir(p) {
				info, e := os.Lstat(p)
				if e != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
					return "", errors.New("capacity target traverses an unavailable directory or symlink")
				}
			}
			return candidate, nil
		}
		if !os.IsNotExist(e) || candidate == "/" {
			return "", errors.New("capacity filesystem cannot be inspected")
		}
	}
}
func checkCapacity(kind, path string, estimate uint64, allowMissing bool) (CapacityCheck, error) {
	c := CapacityCheck{Kind: kind, Path: path, EstimatedAdditionalBytes: estimate, State: "unknown"}
	if !allowMissing {
		st, e := os.Lstat(path)
		if e != nil || !st.IsDir() {
			return c, errors.New("actual storage directory is not accessible")
		}
	}
	observed, e := nearestRealDirectory(path)
	if e != nil {
		return c, e
	}
	c.ObservedPath = observed
	st, e := os.Stat(observed)
	if e != nil {
		return c, e
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok {
		return c, errors.New("filesystem identity unavailable")
	}
	c.Device = uint64(sys.Dev)
	c.AvailableBytes, e = host.AvailableBytes(observed)
	if e != nil {
		return c, errors.New("filesystem free-space query failed")
	}
	c.State = "estimated"
	return c, nil
}
func capacityTotals(checks []CapacityCheck) error {
	totals := map[uint64]uint64{}
	for _, c := range checks {
		if math.MaxUint64-totals[c.Device] < c.EstimatedAdditionalBytes {
			return errors.New("capacity estimate overflow")
		}
		totals[c.Device] += c.EstimatedAdditionalBytes
	}
	failed := false
	for i := range checks {
		c := &checks[i]
		c.FilesystemRequiredBytes = totals[c.Device]
		if c.AvailableBytes < c.FilesystemRequiredBytes {
			c.State = "insufficient"
			failed = true
		} else {
			c.State = "estimated_sufficient"
		}
	}
	if failed {
		return errors.New("insufficient free space for conservative installation estimate")
	}
	return nil
}
func materialSize(r InstallRequest) (uint64, error) {
	if err := noLinks(r.ArtifactPath, false); err != nil {
		return 0, err
	}
	st, e := os.Lstat(r.ArtifactPath)
	if e != nil || !st.Mode().IsRegular() || st.Size() < 0 || st.Size() > 32<<30 {
		return 0, errors.New("bounded regular archived material required")
	}
	return uint64(st.Size()), nil
}
func localInstallCapacity(r InstallRequest) ([]CapacityCheck, error) {
	size, e := materialSize(r)
	if e != nil {
		return nil, e
	}
	checks := []CapacityCheck{}
	// The source is already archived, so it adds no further bytes at this stage.
	for _, spec := range []struct {
		kind, path string
		need       uint64
		missing    bool
	}{{"material_archive", filepath.Dir(r.ArtifactPath), 0, false}, {"private_home", r.PrivateHome, size*5 + (64 << 20), true}} {
		check, e := checkCapacity(spec.kind, spec.path, spec.need, spec.missing)
		checks = append(checks, check)
		if e != nil {
			return checks, e
		}
	}
	return checks, capacityTotals(checks)
}
func (a Adapter) inspectInstallImage(ctx context.Context, r InstallRequest) (ImageEvidence, error) {
	image := ImageEvidence{Reference: r.Recipe.Image}
	out, e := a.runner().Run(ctx, command("docker", "image", "inspect", r.Recipe.Image))
	if e != nil {
		return image, errors.New("prepare the exact digest-pinned image locally before installation")
	}
	if out.Truncated {
		return image, errors.New("image evidence exceeds limit")
	}
	var rows []struct {
		ID           string `json:"Id"`
		OS           string `json:"Os"`
		Architecture string
		Size         *int64
	}
	if json.Unmarshal([]byte(out.Stdout), &rows) != nil || len(rows) != 1 {
		return image, errors.New("invalid image evidence")
	}
	v := rows[0]
	if !strings.HasPrefix(v.ID, "sha256:") || !digestPattern.MatchString(strings.TrimPrefix(v.ID, "sha256:")) || v.OS != "linux" || v.Architecture != "amd64" || v.Size == nil || *v.Size < 0 || *v.Size > 64<<30 {
		return image, errors.New("local pinned image must report linux/amd64, complete ID and bounded size")
	}
	image.ID = v.ID
	image.OS = v.OS
	image.Architecture = v.Architecture
	image.Size = *v.Size
	return image, nil
}
func (a Adapter) localDockerRoot(ctx context.Context) (string, error) {
	// An explicit DOCKER_HOST overrides context selection unless DOCKER_CONTEXT
	// is set. Still inspect the selected context to fail closed on unknown hosts.
	explicit := os.Getenv("DOCKER_HOST")
	if os.Getenv("DOCKER_CONTEXT") == "" && explicit != "" && !strings.HasPrefix(explicit, "unix:///") {
		return "", errors.New("remote Docker endpoint capacity cannot be checked locally")
	}
	out, e := a.runner().Run(ctx, command("docker", "context", "inspect", "--format", "{{json .Endpoints.docker}}"))
	if e != nil || out.Truncated {
		return "", errors.New("Docker endpoint identity unavailable")
	}
	var endpoint struct{ Host string }
	if json.Unmarshal([]byte(out.Stdout), &endpoint) != nil {
		return "", errors.New("Docker endpoint evidence malformed")
	}
	if os.Getenv("DOCKER_CONTEXT") == "" && explicit != "" {
		endpoint.Host = explicit
	}
	if !strings.HasPrefix(endpoint.Host, "unix:///") || strings.ContainsAny(endpoint.Host, "\x00\r\n") {
		return "", errors.New("only a local Unix Docker endpoint supports storage preflight")
	}
	out, e = a.runner().Run(ctx, command("docker", "info", "--format", "{{json .DockerRootDir}}"))
	if e != nil || out.Truncated {
		return "", errors.New("Docker storage location unavailable")
	}
	var root string
	if json.Unmarshal([]byte(out.Stdout), &root) != nil || !filepath.IsAbs(root) || filepath.Clean(root) != root || root == "/" {
		return "", errors.New("Docker storage directory evidence invalid")
	}
	return root, nil
}

// PreflightInstall performs no writes, downloads, authorization or backend setup.
// The service runs it before marking an instance installing. Unknown capacity is
// an explicit manual prerequisite, never an implicit permission to proceed.
func (a Adapter) PreflightInstall(ctx context.Context, r InstallRequest) (InstallPreflight, error) {
	proof := InstallPreflight{State: "unknown", Assumptions: []string{"Already-archived material adds zero bytes to its library filesystem.", "Private HOME estimate: five material sizes (independent copy plus 4x extraction) and 64 MiB margin.", "Docker writable-layer estimate: one local image size plus 4x material and 256 MiB margin; dependencies may require more.", "Requirements on the same device are summed; these are conservative estimates, not quotas or a guarantee against other writers.", "Missing local digest-pinned images and inaccessible/remote daemon storage require manual preparation; no automatic pull or privilege escalation.", "The selected local Unix socket must serve the native host daemon, not a forwarded or virtual-machine daemon; cross-host storage estimates are unsupported."}}
	if e := ctx.Err(); e != nil {
		return proof, e
	}
	if e := validate(r); e != nil {
		return proof, e
	}
	if r.Recipe.Platform != "linux" {
		return proof, fmt.Errorf("%w: Bottles GUI installation requires its own storage/runner preparation", ErrManual)
	}
	var e error
	proof.Capacity, e = localInstallCapacity(r)
	if e != nil {
		return proof, fmt.Errorf("%w: local capacity: %v", ErrManual, e)
	}
	root, e := a.localDockerRoot(ctx)
	if e != nil {
		if ctx.Err() != nil {
			return proof, ctx.Err()
		}
		return proof, fmt.Errorf("%w: %v", ErrManual, e)
	}
	driver, e := a.localDockerDriver(ctx)
	if e != nil {
		if ctx.Err() != nil {
			return proof, ctx.Err()
		}
		return proof, fmt.Errorf("%w: %v", ErrManual, e)
	}
	proof.DockerDriver = driver
	image, e := a.inspectInstallImage(ctx, r)
	if e != nil {
		if ctx.Err() != nil {
			return proof, ctx.Err()
		}
		return proof, fmt.Errorf("%w: %v", ErrManual, e)
	}
	proof.Image = &image
	size, e := materialSize(r)
	if e != nil {
		return proof, e
	}
	check, e := checkCapacity("docker_storage", root, uint64(image.Size)+size*4+(256<<20), false)
	proof.Capacity = append(proof.Capacity, check)
	if e != nil {
		return proof, fmt.Errorf("%w: Docker storage capacity unknown: %v", ErrManual, e)
	}
	if e = capacityTotals(proof.Capacity); e != nil {
		return proof, fmt.Errorf("%w: %v", ErrManual, e)
	}
	proof.State = "estimated_sufficient"
	return proof, nil
}
func (a Adapter) verifyInstallImage(ctx context.Context, r InstallRequest, id string, image ImageEvidence) error {
	out, e := a.runner().Run(ctx, command("docker", "container", "inspect", id))
	if e != nil {
		return e
	}
	var rows []struct {
		ID    string `json:"Id"`
		Image string
	}
	if out.Truncated || json.Unmarshal([]byte(out.Stdout), &rows) != nil || len(rows) != 1 || rows[0].ID != id || rows[0].Image != image.ID {
		return errors.New("target container image differs from inspected pinned image")
	}
	return nil
}

// Classic overlay2/vfs store their writable layer data beneath DockerRootDir.
// Containerd snapshotters, thin pools and other drivers require separate
// capacity adapters; guessing their backing path would validate the wrong FS.
func (a Adapter) localDockerDriver(ctx context.Context) (string, error) {
	out, e := a.runner().Run(ctx, command("docker", "info", "--format", `{"driver":{{json .Driver}},"status":{{json .DriverStatus}}}`))
	if e != nil || out.Truncated {
		return "", errors.New("Docker storage driver evidence unavailable")
	}
	var response struct {
		Driver string          `json:"driver"`
		Status json.RawMessage `json:"status"`
	}
	if json.Unmarshal([]byte(out.Stdout), &response) != nil || (response.Driver != "overlay2" && response.Driver != "vfs") || len(response.Status) == 0 {
		return "", errors.New("only explicitly verified classic overlay2/vfs storage supports this capacity check")
	}
	var status [][]string
	if json.Unmarshal(response.Status, &status) != nil {
		return "", errors.New("Docker storage driver status malformed")
	}
	for _, row := range status {
		if len(row) != 2 {
			return "", errors.New("Docker storage driver status malformed")
		}
		text := strings.ToLower(strings.Join(row, " "))
		if strings.Contains(text, "containerd") || strings.Contains(text, "snapshotter") {
			return "", errors.New("containerd snapshotter capacity requires a separate storage adapter")
		}
	}
	return response.Driver, nil
}
