package backend

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ZZH1124Prime/ZaH/internal/process"
)

type imageCleanupRunner struct {
	calls                     []process.Command
	image                     string
	containers                string
	inspectError, errorRemove bool
	truncated                 bool
}

func (f *imageCleanupRunner) Run(_ context.Context, c process.Command) (process.Result, error) {
	f.calls = append(f.calls, c)
	switch strings.Join(c.Args[:2], " ") {
	case "image inspect":
		if f.inspectError {
			return process.Result{}, errors.New("daemon unavailable")
		}
		return process.Result{Stdout: f.image, Truncated: f.truncated}, nil
	case "container ls":
		return process.Result{Stdout: f.containers}, nil
	case "image rm":
		if f.errorRemove {
			return process.Result{}, errors.New("reference conflict")
		}
		return process.Result{}, nil
	default:
		return process.Result{}, errors.New("unexpected operation")
	}
}
func cleanupProof() SnapshotEvidence {
	return SnapshotEvidence{ImageID: "sha256:" + strings.Repeat("a", 64), ImageCreated: true, SourceInstanceID: "instance-one", OwnershipToken: strings.Repeat("b", 64)}
}
func cleanupInspect(p SnapshotEvidence, mutate func(map[string]any)) string {
	row := map[string]any{"Id": p.ImageID, "RepoTags": []string{}, "RepoDigests": []string{}, "Config": map[string]any{"Labels": map[string]string{"org.zah.snapshot": p.OwnershipToken, "org.zah.snapshot.instance": p.SourceInstanceID}}}
	if mutate != nil {
		mutate(row)
	}
	b, _ := json.Marshal([]any{row})
	return string(b)
}
func TestOwnedSnapshotImageRemovalOnlyExactNoPrune(t *testing.T) {
	proof := cleanupProof()
	f := &imageCleanupRunner{image: cleanupInspect(proof, nil)}
	a := Adapter{f}
	plan, e := a.PlanSnapshotImageRemoval(context.Background(), proof)
	if e != nil || len(f.calls) != 2 {
		t.Fatal(e)
	}
	if _, e := a.RemoveSnapshotImage(context.Background(), plan); e == nil || len(f.calls) != 2 {
		t.Fatal("missing external mutation confirmation accepted")
	}
	plan.ExternalQuietConfirmed = true
	ev, e := a.RemoveSnapshotImage(context.Background(), plan)
	if e != nil || ev.State != "removed" || len(f.calls) != 7 {
		t.Fatalf("%+v %v", ev, e)
	}
	last := f.calls[6]
	if strings.Join(last.Args, " ") != "image rm --no-prune "+proof.ImageID || last.SafeCancel || last.Timeout != 0 {
		t.Fatal(last)
	}
}
func TestSnapshotImageRemovalRejectsForeignReferencedAndChanged(t *testing.T) {
	proof := cleanupProof()
	for _, tc := range []struct {
		name       string
		mutate     func(map[string]any)
		containers string
	}{
		{"foreign_id", func(v map[string]any) { v["Id"] = "sha256:" + strings.Repeat("c", 64) }, ""},
		{"missing_labels", func(v map[string]any) { v["Config"] = nil }, ""},
		{"other_owner", func(v map[string]any) {
			v["Config"] = map[string]any{"Labels": map[string]string{"org.zah.snapshot": "other"}}
		}, ""},
		{"missing_tag_field", func(v map[string]any) { delete(v, "RepoTags") }, ""},
		{"missing_digest_field", func(v map[string]any) { delete(v, "RepoDigests") }, ""},
		{"tagged", func(v map[string]any) { v["RepoTags"] = []string{"production:latest"} }, ""},
		{"digest", func(v map[string]any) { v["RepoDigests"] = []string{"production@sha256:abc"} }, ""},
		{"container", nil, strings.Repeat("c", 64)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &imageCleanupRunner{image: cleanupInspect(proof, tc.mutate), containers: tc.containers}
			if _, e := (Adapter{f}).PlanSnapshotImageRemoval(context.Background(), proof); e == nil {
				t.Fatal("accepted unsafe image")
			}
			for _, c := range f.calls {
				if c.Args[1] == "rm" {
					t.Fatal("removed while planning")
				}
			}
		})
	}
	f := &imageCleanupRunner{image: cleanupInspect(proof, nil)}
	a := Adapter{f}
	plan, e := a.PlanSnapshotImageRemoval(context.Background(), proof)
	if e != nil {
		t.Fatal(e)
	}
	plan.ExternalQuietConfirmed = true
	f.image = cleanupInspect(proof, func(v map[string]any) { v["RepoTags"] = []string{"new-tag"} })
	if _, e = a.RemoveSnapshotImage(context.Background(), plan); e == nil {
		t.Fatal("ignored new tag")
	}
	f.image = cleanupInspect(proof, nil)
	f.errorRemove = true
	if ev, e := a.RemoveSnapshotImage(context.Background(), plan); e == nil || ev.State != "unknown" {
		t.Fatalf("%+v %v", ev, e)
	}
}
func TestSnapshotImageRemovalRejectsUnownedProofAndDaemonFailure(t *testing.T) {
	for _, mutate := range []func(*SnapshotEvidence){func(p *SnapshotEvidence) { p.ImageCreated = false }, func(p *SnapshotEvidence) { p.ImageLoaded = true }, func(p *SnapshotEvidence) { p.OwnershipToken = "" }, func(p *SnapshotEvidence) { p.SourceInstanceID = "" }} {
		p := cleanupProof()
		mutate(&p)
		f := &imageCleanupRunner{}
		if _, e := (Adapter{f}).PlanSnapshotImageRemoval(context.Background(), p); e == nil || len(f.calls) != 0 {
			t.Fatal("accepted naked/load-only identity")
		}
	}
	for _, f := range []*imageCleanupRunner{{inspectError: true}, {truncated: true}} {
		if _, e := (Adapter{f}).PlanSnapshotImageRemoval(context.Background(), cleanupProof()); e == nil {
			t.Fatal("accepted incomplete evidence")
		}
	}
}
