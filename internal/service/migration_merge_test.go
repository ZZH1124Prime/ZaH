package service

import (
	"context"
	"github.com/ZZH1124Prime/ZaH/internal/domain"
	"os"
	"path/filepath"
	"testing"
)

func TestMergeStableIdentitySkipsVersionWithoutChangingExistingData(t *testing.T) {
	ctx := context.Background()
	src, inst := fullSource(t)
	archive := filepath.Join(t.TempDir(), "one.zahbundle")
	if _, e := src.ExportEnvironmentBundle(ctx, EnvironmentBundleInput{ID: inst.ID, Path: archive, ColdConfirmed: true}); e != nil {
		t.Fatal(e)
	}
	dst := catalogService(t)
	first, e := dst.RestoreEnvironmentBundle(ctx, archive, RestoreOptions{Merge: true})
	if e != nil {
		t.Fatal(e)
	}
	familyID := first.IDMap["family:family"]
	instanceID := first.IDMap["instance:"+inst.ID]
	f, e := get[domain.AppFamily](ctx, dst.Store, "family", familyID)
	if e != nil || f.StableID != "family" {
		t.Fatalf("identity lost: %+v %v", f, e)
	}
	current, e := get[domain.AppInstance](ctx, dst.Store, "instance", instanceID)
	if e != nil {
		t.Fatal(e)
	}
	note := filepath.Join(current.PrivateData, "笔记.txt")
	if e = os.WriteFile(note, []byte("destination edit"), 0600); e != nil {
		t.Fatal(e)
	}
	before, e := dst.Store.Get(ctx, "family", familyID)
	if e != nil {
		t.Fatal(e)
	}
	second, e := dst.RestoreEnvironmentBundle(ctx, archive, RestoreOptions{Merge: true})
	if e != nil {
		t.Fatal(e)
	}
	if second.SkippedVersions != 1 || second.Records != 0 || second.IDMap["instance:"+inst.ID] != "" {
		t.Fatalf("not skipped: %+v", second)
	}
	after, _ := dst.Store.Get(ctx, "family", familyID)
	if string(before.Data) != string(after.Data) || before.UpdatedAt != after.UpdatedAt {
		t.Fatal("destination family modified")
	}
	content, e := os.ReadFile(note)
	if e != nil || string(content) != "destination edit" {
		t.Fatal("existing private data overwritten", e)
	}
	children, e := os.ReadDir(filepath.Dir(filepath.Dir(current.PrivateData)))
	if e != nil || len(children) != 1 {
		t.Fatal("skipped version created a directory", e)
	}
	// A different version attaches to the existing stable identity without changing its default.
	release, _ := get[domain.AppRelease](ctx, src.Store, "release", "release")
	release.Version = "2"
	if e = src.Store.Put(ctx, record("release", release.ID, release)); e != nil {
		t.Fatal(e)
	}
	nextArchive := filepath.Join(t.TempDir(), "two.zahbundle")
	if _, e = src.ExportEnvironmentBundle(ctx, EnvironmentBundleInput{ID: inst.ID, Path: nextArchive, ColdConfirmed: true}); e != nil {
		t.Fatal(e)
	}
	third, e := dst.RestoreEnvironmentBundle(ctx, nextArchive, RestoreOptions{Merge: true})
	if e != nil {
		t.Fatal(e)
	}
	r, e := get[domain.AppRelease](ctx, dst.Store, "release", third.IDMap["release:release"])
	if e != nil || r.FamilyID != familyID || r.Version != "2" {
		t.Fatalf("new version not merged: %+v %v", r, e)
	}
	after, _ = dst.Store.Get(ctx, "family", familyID)
	if string(before.Data) != string(after.Data) {
		t.Fatal("merge overwrote default/name")
	}
}

func TestMergeUsesIdentityAndAllVersionDimensions(t *testing.T) {
	ctx := context.Background()
	dst := catalogService(t)
	for _, r := range []domain.Record{record("family", "local", domain.AppFamily{ID: "local", StableID: "portable", Name: "Same"}), record("release", "v1", domain.AppRelease{ID: "v1", FamilyID: "local", Platform: "linux", Arch: "amd64", Version: "1"})} {
		if e := dst.Store.Put(ctx, r); e != nil {
			t.Fatal(e)
		}
	}
	for _, tc := range []struct {
		name, identity, platform, arch, version string
		skip                                    int
		matched                                 bool
	}{
		{"same", "portable", "linux", "amd64", "1", 1, true},
		{"different version", "portable", "linux", "amd64", "2", 0, true},
		{"different platform", "portable", "windows", "amd64", "1", 0, true},
		{"different architecture", "portable", "linux", "unknown", "1", 0, true},
		{"independent same name", "independent", "linux", "amd64", "1", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snap := catalogSnapshot{Records: []domain.Record{record("family", "source", domain.AppFamily{ID: "source", StableID: tc.identity, Name: "Same"}), record("release", "release", domain.AppRelease{ID: "release", FamilyID: "source", Platform: tc.platform, Arch: tc.arch, Version: tc.version})}}
			_, aliases, skip, e := dst.mergeSelection(ctx, snap)
			if e != nil || skip != tc.skip || (aliases["source"] == "local") != tc.matched {
				t.Fatalf("wrong merge %v %d %v", aliases, skip, e)
			}
		})
	}
}
