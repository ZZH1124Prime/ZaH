package backend

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ZZH1124Prime/ZaH/internal/process"
)

func snapshotFixture(t *testing.T, tags []string, mutate func(map[string][]byte)) *bytes.Buffer {
	t.Helper()
	var layer bytes.Buffer
	tw := tar.NewWriter(&layer)
	if err := tw.WriteHeader(&tar.Header{Name: "etc/example", Mode: 0644, Size: 3}); err != nil {
		t.Fatal(err)
	}
	_, _ = tw.Write([]byte("abc"))
	_ = tw.Close()
	lh := sha256.Sum256(layer.Bytes())
	cfg, _ := json.Marshal(map[string]any{"architecture": "amd64", "os": "linux", "rootfs": map[string]any{"type": "layers", "diff_ids": []string{"sha256:" + hex.EncodeToString(lh[:])}}})
	ch := sha256.Sum256(cfg)
	configName := hex.EncodeToString(ch[:]) + ".json"
	manifest, _ := json.Marshal([]dockerManifest{{Config: configName, RepoTags: tags, Layers: []string{"layer/layer.tar"}}})
	files := map[string][]byte{configName: cfg, "manifest.json": manifest, "layer/layer.tar": layer.Bytes()}
	if mutate != nil {
		mutate(files)
	}
	var archive bytes.Buffer
	out := tar.NewWriter(&archive)
	for _, key := range []string{configName, "manifest.json", "layer/layer.tar", "repositories", "evil", "index.json"} {
		b, ok := files[key]
		if !ok {
			continue
		}
		if err := out.WriteHeader(&tar.Header{Name: key, Mode: 0600, Size: int64(len(b))}); err != nil {
			t.Fatal(err)
		}
		if _, err := out.Write(b); err != nil {
			t.Fatal(err)
		}
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	return &archive
}
func writeSnapshot(t *testing.T, b []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "image.tar")
	if err := os.WriteFile(p, b, 0600); err != nil {
		t.Fatal(err)
	}
	return p
}
func TestInspectDockerArchiveChecksAllDigests(t *testing.T) {
	b := snapshotFixture(t, nil, nil).Bytes()
	p := writeSnapshot(t, b)
	info, err := InspectDockerArchive(context.Background(), p, MaxSnapshotBytes)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(b)
	if info.SHA256 != hex.EncodeToString(h[:]) || info.Size != int64(len(b)) || info.Layers != 1 {
		t.Fatalf("%+v", info)
	}
	for _, tc := range []struct {
		name   string
		tags   []string
		mutate func(map[string][]byte)
	}{{"tags", []string{"production:latest"}, nil}, {"corrupted_layer", nil, func(m map[string][]byte) { m["layer/layer.tar"] = []byte("corrupt") }}, {"repositories", nil, func(m map[string][]byte) { m["repositories"] = []byte(`{"prod":{"latest":"x"}}`) }}, {"extra", nil, func(m map[string][]byte) { m["evil"] = []byte("payload") }}, {"oci_index", nil, func(m map[string][]byte) { m["index.json"] = []byte(`{}`) }}} {
		t.Run(tc.name, func(t *testing.T) {
			p := writeSnapshot(t, snapshotFixture(t, tc.tags, tc.mutate).Bytes())
			if _, err := InspectDockerArchive(context.Background(), p, MaxSnapshotBytes); err == nil {
				t.Fatal("accepted unsafe archive")
			}
		})
	}
}
func TestDockerArchiveRejectsTraversalLinkAndDuplicates(t *testing.T) {
	for _, mode := range []string{"traversal", "link", "duplicate", "large"} {
		t.Run(mode, func(t *testing.T) {
			var b bytes.Buffer
			w := tar.NewWriter(&b)
			h := &tar.Header{Name: "manifest.json", Mode: 0600, Size: 2}
			switch mode {
			case "traversal":
				h.Name = "../manifest.json"
			case "link":
				h.Typeflag = tar.TypeSymlink
				h.Size = 0
				h.Linkname = "/etc/passwd"
			case "large":
				h.Size = 1 << 40
			}
			_ = w.WriteHeader(h)
			if h.Size == 2 {
				_, _ = w.Write([]byte("[]"))
			}
			if mode == "duplicate" {
				_ = w.WriteHeader(h)
				_, _ = w.Write([]byte("[]"))
			}
			_ = w.Close()
			if _, err := InspectDockerArchive(context.Background(), writeSnapshot(t, b.Bytes()), MaxSnapshotBytes); err == nil {
				t.Fatal("accepted invalid tar")
			}
		})
	}
}

type snapshotRunner struct {
	calls    []process.Command
	state    string
	archive  []byte
	imageID  string
	existing bool
}

func (f *snapshotRunner) Run(_ context.Context, c process.Command) (process.Result, error) {
	f.calls = append(f.calls, c)
	args := strings.Join(c.Args, "|")
	switch {
	case strings.HasPrefix(args, "container|inspect|"):
		return process.Result{Stdout: f.state}, nil
	case strings.HasPrefix(args, "container|commit|"):
		return process.Result{Stdout: f.imageID}, nil
	case strings.HasPrefix(args, "image|save|--output|"):
		if err := os.WriteFile(c.Args[3], f.archive, 0600); err != nil {
			return process.Result{}, err
		}
		return process.Result{}, nil
	case strings.HasPrefix(args, "container|ls|"):
		return process.Result{}, nil
	case args == "image|ls|--all|--no-trunc|--quiet":
		if f.existing {
			return process.Result{Stdout: f.imageID}, nil
		}
		return process.Result{}, nil
	case strings.HasPrefix(args, "image|load|--input|"):
		return process.Result{}, nil
	case strings.HasPrefix(args, "image|inspect|"):
		return process.Result{Stdout: fmt.Sprintf(`[{"Id":%q,"Architecture":"amd64","Os":"linux"}]`, f.imageID)}, nil
	}
	return process.Result{}, fmt.Errorf("unexpected command: %s", args)
}
func coldState(r InstallRequest, id string, running bool) string {
	return fmt.Sprintf(`[{"Id":%q,"Name":%q,"Config":{"Labels":{"org.zah.instance":%q}},"State":{"Running":%t,"Status":"exited","Pid":0,"StartedAt":"2026-01-01","FinishedAt":"2026-01-02"}}]`, id, "/zah-"+r.InstanceID, r.InstanceID, running)
}
func TestColdExportAndControlledLoad(t *testing.T) {
	r := request(t)
	b := snapshotFixture(t, nil, nil).Bytes()
	p := writeSnapshot(t, b)
	info, err := InspectDockerArchive(context.Background(), p, MaxSnapshotBytes)
	if err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("b", 64)
	f := &snapshotRunner{state: coldState(r, id, false), archive: b, imageID: info.ImageID}
	a := Adapter{f}
	dest := filepath.Join(t.TempDir(), "snapshot.tar")
	ev, err := a.ExportLinuxSnapshot(context.Background(), r, id, dest)
	if err != nil {
		t.Fatal(err)
	}
	if ev.SourceInstanceID != r.InstanceID || !digestPattern.MatchString(ev.OwnershipToken) || ev.CompleteInstance || !ev.ImageCreated || ev.State != "image_snapshot_verified" || ev.Archive.SHA256 != info.SHA256 {
		t.Fatalf("%+v", ev)
	}
	foundOwnership := false
	for _, c := range f.calls {
		if len(c.Args) > 1 && c.Args[1] == "commit" {
			foundOwnership = strings.Join(c.Args, "|") == "container|commit|--change|LABEL org.zah.snapshot="+ev.OwnershipToken+"|--change|LABEL org.zah.snapshot.instance="+r.InstanceID+"|"+id
		}
	}
	if !foundOwnership {
		t.Fatal("commit omitted exact snapshot ownership")
	}
	if _, err = a.ExportLinuxSnapshot(context.Background(), r, id, dest); err == nil {
		t.Fatal("overwrote snapshot")
	}
	plan, err := a.PlanLinuxRestore(context.Background(), r, dest, info.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	stage := t.TempDir()
	if err = os.Chmod(stage, 0700); err != nil {
		t.Fatal(err)
	}
	loaded, err := a.LoadLinuxSnapshot(context.Background(), plan, stage)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.CompleteInstance || !loaded.ImageLoaded || loaded.State != "image_available_manual_environment_rebuild" {
		t.Fatalf("%+v", loaded)
	}
	f.existing = true
	f.calls = nil
	loaded, err = a.LoadLinuxSnapshot(context.Background(), plan, stage)
	if err != nil || loaded.ImageLoaded {
		t.Fatalf("%+v %v", loaded, err)
	}
	for _, call := range f.calls {
		if len(call.Args) > 1 && call.Args[0] == "image" && call.Args[1] == "load" {
			t.Fatal("reloaded existing image")
		}
	}
}
func TestColdExportRejectsRunningContainer(t *testing.T) {
	r := request(t)
	id := strings.Repeat("a", 64)
	f := &snapshotRunner{state: coldState(r, id, true)}
	_, err := (Adapter{f}).ExportLinuxSnapshot(context.Background(), r, id, filepath.Join(t.TempDir(), "image.tar"))
	if err == nil || len(f.calls) != 1 {
		t.Fatalf("%v calls=%d", err, len(f.calls))
	}
}
func TestRestoreRevalidatesMaterialBeforeAnyDockerCall(t *testing.T) {
	r := request(t)
	b := snapshotFixture(t, nil, nil).Bytes()
	p := writeSnapshot(t, b)
	info, err := InspectDockerArchive(context.Background(), p, MaxSnapshotBytes)
	if err != nil {
		t.Fatal(err)
	}
	f := &snapshotRunner{}
	a := Adapter{f}
	plan, err := a.PlanLinuxRestore(context.Background(), r, p, info.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(p, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = a.LoadLinuxSnapshot(context.Background(), plan, t.TempDir()); err == nil || len(f.calls) != 0 {
		t.Fatal("loaded changed material")
	}
}

func TestSnapshotFailureRetainsCreatedOwnershipEvidence(t *testing.T) {
	r := request(t)
	id := strings.Repeat("a", 64)
	f := &snapshotRunner{state: coldState(r, id, false), archive: []byte("corrupted save output"), imageID: "sha256:" + strings.Repeat("b", 64)}
	ev, err := (Adapter{f}).ExportLinuxSnapshot(context.Background(), r, id, filepath.Join(t.TempDir(), "image.tar"))
	if err == nil || !ev.ImageCreated || ev.ImageID != f.imageID || ev.SourceInstanceID != r.InstanceID || !digestPattern.MatchString(ev.OwnershipToken) {
		t.Fatalf("lost creation evidence: %+v %v", ev, err)
	}
	for _, c := range f.calls {
		if len(c.Args) > 1 && (c.Args[1] == "rm" || c.Args[1] == "prune") {
			t.Fatal("destructive rollback")
		}
	}
}

func TestNormalizeDockerSaveBlobLayout(t *testing.T) {
	original := snapshotFixture(t, nil, nil).Bytes()
	want, err := InspectDockerArchive(context.Background(), writeSnapshot(t, original), MaxSnapshotBytes)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(bytes.NewReader(original))
	files := map[string][]byte{}
	for {
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			t.Fatal(e)
		}
		b, e := io.ReadAll(tr)
		if e != nil {
			t.Fatal(e)
		}
		files[h.Name] = b
	}
	var m []dockerManifest
	if err = json.Unmarshal(files["manifest.json"], &m); err != nil {
		t.Fatal(err)
	}
	oldConfig, oldLayer := m[0].Config, m[0].Layers[0]
	m[0].Config = "blobs/sha256/" + strings.TrimSuffix(oldConfig, ".json")
	lh := sha256.Sum256(files[oldLayer])
	m[0].Layers[0] = "blobs/sha256/" + hex.EncodeToString(lh[:])
	for _, corrupt := range []bool{false, true} {
		var b bytes.Buffer
		w := tar.NewWriter(&b)
		mf, _ := json.Marshal(m)
		layer := append([]byte(nil), files[oldLayer]...)
		if corrupt {
			layer[0] ^= 1
		}
		for name, data := range map[string][]byte{m[0].Config: files[oldConfig], m[0].Layers[0]: layer, "manifest.json": mf, "index.json": []byte(`{"schemaVersion":2}`), "oci-layout": []byte(`{"imageLayoutVersion":"1.0.0"}`)} {
			if err = w.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(data))}); err != nil {
				t.Fatal(err)
			}
			if _, err = w.Write(data); err != nil {
				t.Fatal(err)
			}
		}
		if err = w.Close(); err != nil {
			t.Fatal(err)
		}
		p := writeSnapshot(t, b.Bytes())
		err = normalizeSavedDockerImage(context.Background(), p)
		if corrupt {
			if err == nil {
				t.Fatal("accepted corrupt layer")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		got, e := InspectDockerArchive(context.Background(), p, MaxSnapshotBytes)
		if e != nil || got.ImageID != want.ImageID {
			t.Fatalf("%+v %v", got, e)
		}
	}
}
