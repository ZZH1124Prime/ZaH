package backend

import (
	"archive/tar"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/ZZH1124Prime/ZaH/internal/host"
)

const MaxSnapshotBytes int64 = 64 << 30
const maxSnapshotEntries = 50000
const maxSnapshotMetadata = 8 << 20

// DockerArchive describes a validated, single-image, tagless classic Docker-save
// archive. Rootfs layers stay opaque tar byte streams and are never host-extracted.
type DockerArchive struct {
	Path         string `json:"path"`
	SHA256       string `json:"sha256"`
	Size         int64  `json:"size"`
	ImageID      string `json:"image_id"`
	Architecture string `json:"architecture"`
	OS           string `json:"os"`
	Layers       int    `json:"layers"`
}

// SnapshotEvidence always distinguishes an image-only snapshot from a complete
// restorable instance. Created images are retained and explicitly identified.
type SnapshotEvidence struct {
	Archive          DockerArchive `json:"archive"`
	ImageID          string        `json:"image_id,omitempty"`
	State            string        `json:"state"`
	ImageCreated     bool          `json:"image_created"`
	SourceInstanceID string        `json:"source_instance_id,omitempty"`
	OwnershipToken   string        `json:"ownership_token,omitempty"`
	ImageLoaded      bool          `json:"image_loaded"`
	ColdWitness      string        `json:"cold_witness,omitempty"`
	CompleteInstance bool          `json:"complete_instance"`
	Excluded         []string      `json:"excluded"`
}

// LinuxRestorePlan is immutable at service level. No archived runtime command,
// mount, account or Docker inspect structure is replayed by this plan.
type LinuxRestorePlan struct {
	Request         InstallRequest `json:"request"`
	Archive         DockerArchive  `json:"archive"`
	TargetName      string         `json:"target_name"`
	State           string         `json:"state"`
	RequiredActions []string       `json:"required_actions"`
}

type archiveFile struct {
	sum  string
	size int64
	data []byte
}
type dockerManifest struct {
	Config   string
	RepoTags []string
	Layers   []string
}

// InspectDockerArchive verifies all data before Docker sees the archive. It
// rejects tags and alternate OCI indexes to prevent changes to existing tags.
// A future OCI importer must normalize and validate its entire descriptor graph.
func InspectDockerArchive(ctx context.Context, p string, maxBytes int64) (DockerArchive, error) {
	if maxBytes <= 0 || maxBytes > MaxSnapshotBytes {
		maxBytes = MaxSnapshotBytes
	}
	if err := noLinks(p, false); err != nil {
		return DockerArchive{}, err
	}
	f, err := os.Open(p)
	if err != nil {
		return DockerArchive{}, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return DockerArchive{}, err
	}
	if !st.Mode().IsRegular() || st.Size() <= 0 || st.Size() > maxBytes {
		return DockerArchive{}, errors.New("snapshot file type or size exceeds limit")
	}
	h := sha256.New()
	reader := io.TeeReader(io.LimitReader(contextReader{ctx, f}, maxBytes+1), h)
	tr := tar.NewReader(reader)
	files := map[string]archiveFile{}
	seen := map[string]bool{}
	var total int64
	for n := 0; ; n++ {
		if n > maxSnapshotEntries {
			return DockerArchive{}, errors.New("snapshot entry count exceeds limit")
		}
		hdr, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return DockerArchive{}, e
		}
		key := strings.TrimSuffix(hdr.Name, "/")
		if key == "" || !path.IsAbs(key) && path.Clean(key) != key || path.IsAbs(key) || key == ".." || strings.HasPrefix(key, "../") || strings.ContainsAny(key, "\\\x00") || seen[key] {
			return DockerArchive{}, errors.New("unsafe or duplicate snapshot path")
		}
		seen[key] = true
		if hdr.Typeflag == tar.TypeDir {
			if hdr.Size != 0 {
				return DockerArchive{}, errors.New("invalid archive directory")
			}
			continue
		}
		if hdr.Typeflag != tar.TypeReg || hdr.Mode&07000 != 0 || hdr.Size < 0 || hdr.Size > maxBytes-total {
			return DockerArchive{}, errors.New("unsafe or oversized snapshot entry")
		}
		total += hdr.Size
		fh := sha256.New()
		var data []byte
		metadata := key == "manifest.json" || key == "repositories" || strings.HasSuffix(key, ".json") || strings.HasSuffix(key, "/json") || strings.HasSuffix(key, "/VERSION")
		if metadata {
			if hdr.Size > maxSnapshotMetadata {
				return DockerArchive{}, errors.New("snapshot metadata exceeds limit")
			}
			data, e = io.ReadAll(io.TeeReader(tr, fh))
		} else {
			_, e = io.Copy(fh, tr)
		}
		if e != nil {
			return DockerArchive{}, e
		}
		files[key] = archiveFile{hex.EncodeToString(fh.Sum(nil)), hdr.Size, data}
	}
	// tar EOF can occur before physical EOF. Only zero padding may follow.
	buf := make([]byte, 32<<10)
	for {
		n, e := reader.Read(buf)
		for _, b := range buf[:n] {
			if b != 0 {
				return DockerArchive{}, errors.New("trailing snapshot content")
			}
		}
		if e == io.EOF {
			break
		}
		if e != nil {
			return DockerArchive{}, e
		}
	}
	if _, err = f.Seek(0, io.SeekCurrent); err != nil {
		return DockerArchive{}, err
	}
	after, err := f.Stat()
	if err != nil || after.Size() != st.Size() || !after.ModTime().Equal(st.ModTime()) {
		return DockerArchive{}, errors.New("snapshot changed during inspection")
	}
	var manifest []dockerManifest
	mf, ok := files["manifest.json"]
	if !ok || json.Unmarshal(mf.data, &manifest) != nil || len(manifest) != 1 {
		return DockerArchive{}, errors.New("single-image classic Docker manifest required")
	}
	m := manifest[0]
	if len(m.RepoTags) != 0 {
		return DockerArchive{}, errors.New("snapshot must not contain image tags")
	}
	if len(m.Layers) == 0 || len(m.Layers) > 4096 {
		return DockerArchive{}, errors.New("invalid snapshot layer count")
	}
	if repos, ok := files["repositories"]; ok {
		var values map[string]json.RawMessage
		if json.Unmarshal(repos.data, &values) != nil || len(values) != 0 {
			return DockerArchive{}, errors.New("legacy repository tags are prohibited")
		}
	}
	cfg, ok := files[m.Config]
	if !ok || len(cfg.data) == 0 {
		return DockerArchive{}, errors.New("image configuration missing")
	}
	id := "sha256:" + cfg.sum
	if m.Config != cfg.sum+".json" {
		return DockerArchive{}, errors.New("unsupported image configuration naming; OCI archives require normalization")
	}
	var config struct {
		Architecture string `json:"architecture"`
		OS           string `json:"os"`
		RootFS       struct {
			Type    string   `json:"type"`
			DiffIDs []string `json:"diff_ids"`
		} `json:"rootfs"`
	}
	if json.Unmarshal(cfg.data, &config) != nil || config.Architecture != "amd64" || config.OS != "linux" || config.RootFS.Type != "layers" || len(config.RootFS.DiffIDs) != len(m.Layers) {
		return DockerArchive{}, errors.New("incompatible image architecture or rootfs")
	}
	allowed := map[string]bool{"manifest.json": true, "repositories": true, m.Config: true}
	layers := map[string]bool{}
	for i, p := range m.Layers {
		v, ok := files[p]
		if !ok || v.size == 0 || layers[p] || "sha256:"+v.sum != config.RootFS.DiffIDs[i] {
			return DockerArchive{}, errors.New("missing, duplicate or corrupted snapshot layer")
		}
		layers[p] = true
		allowed[p] = true
		dir := path.Dir(p)
		if dir != "." {
			allowed[path.Join(dir, "json")] = true
			allowed[path.Join(dir, "VERSION")] = true
		}
	}
	for key := range files {
		if !allowed[key] {
			return DockerArchive{}, errors.New("unrecognized snapshot metadata or payload")
		}
	}
	return DockerArchive{Path: p, SHA256: hex.EncodeToString(h.Sum(nil)), Size: st.Size(), ImageID: id, Architecture: config.Architecture, OS: config.OS, Layers: len(m.Layers)}, nil
}

func noLinks(p string, allowMissingLeaf bool) error {
	if !filepath.IsAbs(p) || filepath.Clean(p) != p || p == "/" || strings.ContainsAny(p, "\x00\r\n") {
		return errors.New("normalized absolute path required")
	}
	leaf := p
	for p != "/" {
		st, err := os.Lstat(p)
		if err != nil {
			if allowMissingLeaf && p == leaf && os.IsNotExist(err) {
				p = filepath.Dir(p)
				continue
			}
			return err
		}
		if st.Mode()&os.ModeSymlink != 0 {
			return errors.New("snapshot path traverses a symlink")
		}
		if p != leaf && !st.IsDir() {
			return errors.New("snapshot ancestor is not a directory")
		}
		p = filepath.Dir(p)
	}
	return nil
}
func imageOnlyEvidence() SnapshotEvidence {
	return SnapshotEvidence{State: "incomplete", CompleteInstance: false, Excluded: []string{"private HOME and container volumes", "external file bindings", "runner and host integration", "application functional verification"}}
}

// coldWitness verifies exact ownership and a stopped Docker state. It records
// start/finish identity so a stop/start cycle during export is detectable.
func (a Adapter) coldWitness(ctx context.Context, r InstallRequest, id string) (string, error) {
	if !digestPattern.MatchString(id) {
		return "", errors.New("full managed container ID required")
	}
	out, err := a.runner().Run(ctx, command("docker", "container", "inspect", id))
	if err != nil {
		return "", err
	}
	if out.Truncated {
		return "", errors.New("container inspection exceeds limit")
	}
	var rows []struct {
		ID     string `json:"Id"`
		Name   string
		Config struct{ Labels map[string]string }
		State  struct {
			Status     string
			Running    bool
			Paused     bool
			Restarting bool
			Dead       bool
			Pid        int
			StartedAt  string
			FinishedAt string
		}
	}
	if json.Unmarshal([]byte(out.Stdout), &rows) != nil || len(rows) != 1 {
		return "", errors.New("invalid container inspection")
	}
	v := rows[0]
	if v.ID != id || v.Name != "/"+name(r.InstanceID) || v.Config.Labels["org.zah.instance"] != r.InstanceID {
		return "", errors.New("snapshot resource ownership changed")
	}
	if v.State.Running || v.State.Paused || v.State.Restarting || v.State.Dead || v.State.Pid != 0 || (v.State.Status != "exited" && v.State.Status != "created") {
		return "", errors.New("cold snapshot requires a stopped container and stopped external writers")
	}
	b, _ := json.Marshal(v.State)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}

// ExportLinuxSnapshot commits only the owned stopped container, saves by image
// ID (no tags), validates the resulting archive and publishes without overwrite.
// The caller holds environment/reference locks and confirms external writers
// stopped. The new untagged image is retained in evidence even on later failure.
func (a Adapter) ExportLinuxSnapshot(ctx context.Context, r InstallRequest, resourceID, destination string) (SnapshotEvidence, error) {
	ev := imageOnlyEvidence()
	if err := validate(r); err != nil {
		return ev, err
	}
	if r.Recipe.Platform != "linux" {
		return ev, ErrUnsupported
	}
	if a.Runner == nil && os.Geteuid() == 0 {
		return ev, errors.New("ordinary user required")
	}
	if err := noLinks(destination, true); err != nil {
		return ev, err
	}
	if _, err := os.Lstat(destination); !os.IsNotExist(err) {
		return ev, errors.New("snapshot destination already exists or is inaccessible")
	}
	witness, err := a.coldWitness(ctx, r, resourceID)
	if err != nil {
		return ev, err
	}
	ev.ColdWitness = witness
	// This check only concerns archive storage. Docker data-root capacity must be
	// checked separately by the caller; remote/privileged daemon paths are unknown.
	if free, e := host.AvailableBytes(filepath.Dir(destination)); e != nil {
		return ev, e
	} else if free < 1<<20 {
		return ev, errors.New("insufficient archive filesystem space")
	}
	dir, err := os.MkdirTemp(filepath.Dir(destination), ".zah-snapshot-")
	if err != nil {
		return ev, err
	}
	defer os.RemoveAll(dir)
	tokenBytes := make([]byte, 32)
	if _, err = rand.Read(tokenBytes); err != nil {
		return ev, err
	}
	ev.SourceInstanceID = r.InstanceID
	ev.OwnershipToken = hex.EncodeToString(tokenBytes)
	c := command("docker", "container", "commit", "--change", "LABEL org.zah.snapshot="+ev.OwnershipToken, "--change", "LABEL org.zah.snapshot.instance="+r.InstanceID, resourceID)
	c.SafeCancel = false
	c.Timeout = 0
	out, err := a.runner().Run(ctx, c)
	if err != nil {
		return ev, err
	}
	imageID := strings.TrimSpace(out.Stdout)
	if out.Truncated || !strings.HasPrefix(imageID, "sha256:") || !digestPattern.MatchString(strings.TrimPrefix(imageID, "sha256:")) {
		return ev, errors.New("commit returned unknown image identity; inspect daemon before retry")
	}
	ev.ImageID = imageID
	ev.ImageCreated = true
	after, err := a.coldWitness(ctx, r, resourceID)
	if err != nil || after != witness {
		if err == nil {
			err = errors.New("container state changed during snapshot")
		}
		return ev, err
	}
	partial := filepath.Join(dir, "image.tar.partial")
	c = command("docker", "image", "save", "--output", partial, imageID)
	c.SafeCancel = false
	c.Timeout = 0
	if _, err = a.runner().Run(ctx, c); err != nil {
		return ev, err
	}
	if err = normalizeSavedDockerImage(ctx, partial); err != nil {
		return ev, err
	}
	archive, err := InspectDockerArchive(ctx, partial, MaxSnapshotBytes)
	if err != nil {
		return ev, err
	}
	if archive.ImageID != imageID {
		return ev, errors.New("saved image identity differs from committed image")
	}
	after, err = a.coldWitness(ctx, r, resourceID)
	if err != nil || after != witness {
		if err == nil {
			err = errors.New("container restarted during snapshot")
		}
		return ev, err
	}
	if err = os.Chmod(partial, 0600); err != nil {
		return ev, err
	}
	f, err := os.OpenFile(partial, os.O_RDONLY, 0)
	if err != nil {
		return ev, err
	}
	err = f.Sync()
	f.Close()
	if err != nil {
		return ev, err
	}
	// Hard-link publication atomically refuses an existing final name. The linked
	// inode is our independent staged archive, never the user's source material.
	if err = os.Link(partial, destination); err != nil {
		return ev, err
	}
	if err = syncDirectory(filepath.Dir(destination)); err != nil {
		ev.State = "published_durability_unknown"
		archive.Path = destination
		ev.Archive = archive
		return ev, err
	}
	archive.Path = destination
	ev.Archive = archive
	ev.State = "image_snapshot_verified"
	return ev, nil
}
func syncDirectory(p string) error {
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

// PlanLinuxRestore validates local bytes only and identifies remaining manual
// target-user/HOME integration. It does not load or launch the image.
func (a Adapter) PlanLinuxRestore(ctx context.Context, r InstallRequest, archivePath, expectedSHA256 string) (LinuxRestorePlan, error) {
	if err := validate(r); err != nil {
		return LinuxRestorePlan{}, err
	}
	if r.Recipe.Platform != "linux" || !digestPattern.MatchString(expectedSHA256) {
		return LinuxRestorePlan{}, errors.New("Linux restore and expected archive digest required")
	}
	archive, err := InspectDockerArchive(ctx, archivePath, MaxSnapshotBytes)
	if err != nil {
		return LinuxRestorePlan{}, err
	}
	if archive.SHA256 != expectedSHA256 {
		return LinuxRestorePlan{}, errors.New("archive digest mismatch")
	}
	return LinuxRestorePlan{Request: r, Archive: archive, TargetName: name(r.InstanceID), State: "image_load_then_manual_rebuild", RequiredActions: []string{"Load the verified untagged image without restoring old tags.", "Rebuild a new Distrobox with the target user, independent HOME and explicitly approved bindings using supported upstream workflow.", "Restore HOME/volume data separately; do not rewrite passwd, initialization markers or private upstream databases blindly.", "Use VerifyManualLinuxRestore before marking the new instance ready; verify files, audio and app capabilities separately."}}, nil
}

// LoadLinuxSnapshot imports a privately staged, revalidated tagless image. It
// never overwrites catalog entries, restores a container or marks an app ready.
func (a Adapter) LoadLinuxSnapshot(ctx context.Context, plan LinuxRestorePlan, stagingDir string) (SnapshotEvidence, error) {
	ev := imageOnlyEvidence()
	if err := validate(plan.Request); err != nil {
		return ev, err
	}
	if plan.Request.Recipe.Platform != "linux" || plan.TargetName != name(plan.Request.InstanceID) || !digestPattern.MatchString(plan.Archive.SHA256) {
		return ev, errors.New("invalid restore plan")
	}
	if a.Runner == nil && os.Geteuid() == 0 {
		return ev, errors.New("ordinary user required")
	}
	if err := noLinks(stagingDir, false); err != nil {
		return ev, err
	}
	st, err := os.Stat(stagingDir)
	if err != nil || !st.IsDir() || st.Mode().Perm()&0077 != 0 {
		return ev, errors.New("private staging directory required")
	}
	dir, err := os.MkdirTemp(stagingDir, "image-load-")
	if err != nil {
		return ev, err
	}
	defer os.RemoveAll(dir)
	staged := filepath.Join(dir, "image.tar")
	if err = copySnapshot(ctx, plan.Archive.Path, staged, MaxSnapshotBytes); err != nil {
		return ev, err
	}
	archive, err := InspectDockerArchive(ctx, staged, MaxSnapshotBytes)
	if err != nil {
		return ev, err
	}
	if archive.SHA256 != plan.Archive.SHA256 || archive.ImageID != plan.Archive.ImageID || archive.Size != plan.Archive.Size {
		return ev, errors.New("restore material changed after planning")
	}
	ev.Archive = plan.Archive
	ev.ImageID = archive.ImageID
	out, err := a.runner().Run(ctx, command("docker", "container", "ls", "--all", "--filter", "name=^/"+plan.TargetName+"$", "--format", "{{.ID}}"))
	if err != nil {
		return ev, err
	}
	if out.Truncated || strings.TrimSpace(out.Stdout) != "" {
		return ev, errors.New("target environment name already exists or listing incomplete")
	}
	out, err = a.runner().Run(ctx, command("docker", "image", "ls", "--all", "--no-trunc", "--quiet"))
	if err != nil {
		return ev, err
	}
	if out.Truncated {
		return ev, errors.New("image inventory exceeds limit")
	}
	exists := false
	for _, id := range strings.Fields(out.Stdout) {
		if id == archive.ImageID {
			exists = true
		}
	}
	if !exists {
		c := command("docker", "image", "load", "--input", staged)
		c.SafeCancel = false
		c.Timeout = 0
		if _, err = a.runner().Run(ctx, c); err != nil {
			return ev, err
		}
		ev.ImageLoaded = true
	}
	out, err = a.runner().Run(ctx, command("docker", "image", "inspect", archive.ImageID))
	if err != nil {
		return ev, err
	}
	if out.Truncated {
		return ev, errors.New("image inspection exceeds limit")
	}
	var rows []struct {
		ID           string `json:"Id"`
		Architecture string
		OS           string `json:"Os"`
	}
	if json.Unmarshal([]byte(out.Stdout), &rows) != nil || len(rows) != 1 || rows[0].ID != archive.ImageID || rows[0].Architecture != "amd64" || rows[0].OS != "linux" {
		return ev, errors.New("loaded image identity or architecture mismatch")
	}
	ev.State = "image_available_manual_environment_rebuild"
	return ev, nil
}
func copySnapshot(ctx context.Context, src, dst string, max int64) error {
	if err := noLinks(src, false); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() || st.Size() > max {
		return errors.New("snapshot source type or size invalid")
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer out.Close()
	n, err := io.Copy(out, io.LimitReader(contextReader{ctx, in}, max+1))
	if err != nil {
		return err
	}
	if n > max {
		return errors.New("snapshot exceeds size limit")
	}
	return out.Sync()
}

// VerifyManualLinuxRestore validates a user-rebuilt *new* environment. It never
// adopts a foreign label/name or changes ownership. ImageID must be the verified
// snapshot's exact image identity and the ordinary-user entry must exist.
func (a Adapter) VerifyManualLinuxRestore(ctx context.Context, r InstallRequest, resourceID, imageID string) (Evidence, error) {
	if err := validate(r); err != nil {
		return Evidence{}, err
	}
	if r.Recipe.Platform != "linux" || !strings.HasPrefix(imageID, "sha256:") || !digestPattern.MatchString(strings.TrimPrefix(imageID, "sha256:")) {
		return Evidence{}, errors.New("verified Linux snapshot image required")
	}
	ev, err := a.Inspect(ctx, r, resourceID)
	if err != nil {
		return ev, err
	}
	out, err := a.runner().Run(ctx, command("docker", "container", "inspect", "--format", "{{.Image}}", resourceID))
	if err != nil {
		return ev, err
	}
	if out.Truncated || strings.TrimSpace(out.Stdout) != imageID {
		return Evidence{State: "changed"}, errors.New("restored environment image differs from snapshot")
	}
	ev, err = a.ExecuteStep(ctx, Step{Kind: "verify_entry", Request: r, ResourceID: resourceID, Mode: "automatic"})
	if err != nil {
		return ev, err
	}
	return ev, nil
}

// VerifyColdSnapshot is the final gate before publishing a larger instance bundle.
func (a Adapter) VerifyColdSnapshot(ctx context.Context, r InstallRequest, id, witness string) error {
	if witness == "" {
		return errors.New("cold state witness required")
	}
	now, err := a.coldWitness(ctx, r, id)
	if err != nil {
		return err
	}
	if now != witness {
		return errors.New("container state changed while packaging private data")
	}
	return nil
}
