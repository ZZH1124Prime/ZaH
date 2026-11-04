package service

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"github.com/ZZH1124Prime/ZaH/internal/domain"
	"github.com/ZZH1124Prime/ZaH/internal/installmedia"
	"github.com/ZZH1124Prime/ZaH/internal/recipe"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func guidedFixture(t *testing.T, s *Service) string {
	t.Helper()
	data := make([]byte, 128)
	copy(data, "\x7fELF")
	data[4] = 2
	data[5] = 1
	copy(data[8:], "AI\x02")
	binary.LittleEndian.PutUint16(data[18:], 62)
	p := filepath.Join(t.TempDir(), "inert.AppImage")
	if e := os.WriteFile(p, data, 0600); e != nil {
		t.Fatal(e)
	}
	plan := rpcValue[domain.Plan](t, s, "plan.create", map[string]any{"operation": "artifact.import", "input": map[string]string{"path": p}})
	if _, e := s.Execute(context.Background(), plan); e != nil {
		t.Fatal(e)
	}
	rows, e := s.Store.List(context.Background(), "artifact")
	if e != nil {
		t.Fatal(e)
	}
	return rows[0].ID
}
func TestGuidedSetupAtomicRetryAndExistingFamily(t *testing.T) {
	s := catalogService(t)
	artifact := guidedFixture(t, s)
	info := rpcValue[installmedia.Info](t, s, "installation.inspect", map[string]string{"artifact_id": artifact})
	if info.Format != "appimage" {
		t.Fatal(info)
	}
	in := installConfiguration{RequestID: domain.NewID(), ArtifactID: artifact, Name: "应用", Version: "1", Base: "ubuntu@sha256:" + strings.Repeat("a", 64), Entry: "squashfs-root/AppRun", Mode: "native"}
	v := rpcValue[domain.AppInstance](t, s, "installation.configure", in)
	configured, err := get[recipe.Recipe](context.Background(), s.Store, "recipe", v.RecipeRevision)
	if err != nil || strings.Join(configured.Dependencies, ",") != "python3,libglib2.0-bin,gdebi,synaptic,thunar,libopengl0" {
		t.Fatalf("missing native installation/desktop dependencies: %+v %v", configured.Dependencies, err)
	}
	again := rpcValue[domain.AppInstance](t, s, "installation.configure", in)
	if v.ID != again.ID || v.State != "unprepared" {
		t.Fatal(v, again)
	}
	for _, kind := range []string{"family", "release", "instance", "environment", "recipe"} {
		rs, e := s.Store.List(context.Background(), kind)
		if e != nil || len(rs) != 1 {
			t.Fatalf("%s %v %v", kind, rs, e)
		}
	}
	in.Version = "changed"
	raw, _ := json.Marshal(in)
	if _, e := s.Handle(context.Background(), "installation.configure", raw); e == nil || errors.Is(e, domain.ErrConfigurationRejected) {
		t.Fatal("same key changed request accepted")
	}
	families, _ := s.Store.List(context.Background(), "family")
	in.RequestID = domain.NewID()
	in.FamilyID = families[0].ID
	in.Version = "2"
	_ = rpcValue[domain.AppInstance](t, s, "installation.configure", in)
	families, _ = s.Store.List(context.Background(), "family")
	if len(families) != 1 {
		t.Fatal("duplicated family")
	}
	plans, _ := s.Store.List(context.Background(), "plan")
	if len(plans) != 1 {
		t.Fatal("setup must not submit an installation")
	}
}
func TestGuidedSetupRejectsBeforeAnyCatalogWrite(t *testing.T) {
	s := catalogService(t)
	artifact := guidedFixture(t, s)
	for _, bad := range []installConfiguration{{RequestID: domain.NewID(), ArtifactID: artifact, Name: "应用", Version: "1", Base: "ubuntu:latest", Entry: "squashfs-root/AppRun", Mode: "native"}, {RequestID: domain.NewID(), ArtifactID: artifact, Name: "应用", Version: "1", Base: "ubuntu@sha256:" + strings.Repeat("a", 64), Entry: "squashfs-root/AppRun", Mode: "installer"}} {
		raw, _ := json.Marshal(bad)
		if _, e := s.Handle(context.Background(), "installation.configure", raw); e == nil {
			t.Fatal("invalid setup accepted")
		}
	}
	for _, kind := range []string{"family", "release", "instance", "environment", "recipe"} {
		rs, _ := s.Store.List(context.Background(), kind)
		if len(rs) != 0 {
			t.Fatal("partial setup", kind)
		}
	}
}

func TestApplicationPlatformAndDuplicateVersionGuard(t *testing.T) {
	s := catalogService(t)
	artifact := guidedFixture(t, s)
	family := rpcValue[domain.AppFamily](t, s, "family.create", map[string]string{"name": "Independent App", "platform": "linux"})
	if family.Platform != "linux" || family.StableID != family.ID {
		t.Fatal(family)
	}
	in := installConfiguration{RequestID: domain.NewID(), ArtifactID: artifact, FamilyID: family.ID, Name: family.Name, Version: "1", Base: "ubuntu@sha256:" + strings.Repeat("a", 64), Entry: "squashfs-root/AppRun", Mode: "native"}
	_ = rpcValue[domain.AppInstance](t, s, "installation.configure", in)
	in.RequestID = domain.NewID()
	raw, _ := json.Marshal(in)
	if _, e := s.Handle(context.Background(), "installation.configure", raw); !errors.Is(e, domain.ErrConfigurationRejected) {
		t.Fatal("same application version overwritten")
	}
	other := rpcValue[domain.AppFamily](t, s, "family.create", map[string]string{"name": "Independent App", "platform": "windows"})
	if other.StableID == family.StableID {
		t.Fatal("display names equated")
	}
	in.FamilyID = other.ID
	raw, _ = json.Marshal(in)
	if _, e := s.Handle(context.Background(), "installation.configure", raw); e == nil {
		t.Fatal("platform mismatch accepted")
	}
	records, _ := s.Store.List(context.Background(), "instance")
	if len(records) != 1 {
		t.Fatal("partial registration")
	}
}

func TestGuidedSetupStorageFailureDoesNotAuthorizeANewIdentity(t *testing.T) {
	s := catalogService(t)
	artifact := guidedFixture(t, s)
	if _, e := s.Store.DB.Exec(`CREATE TRIGGER reject_fixture_instance BEFORE INSERT ON app_instances BEGIN SELECT RAISE(ABORT, 'fixture commit failure'); END`); e != nil {
		t.Fatal(e)
	}
	in := installConfiguration{RequestID: domain.NewID(), ArtifactID: artifact, Name: "App", Version: "1", Base: "ubuntu@sha256:" + strings.Repeat("a", 64), Entry: "squashfs-root/AppRun", Mode: "native"}
	raw, _ := json.Marshal(in)
	_, e := s.Handle(context.Background(), "installation.configure", raw)
	if e == nil || errors.Is(e, domain.ErrConfigurationRejected) {
		t.Fatalf("storage failure classified as editable validation: %v", e)
	}
	rows, e := s.Store.List(context.Background(), "instance")
	if e != nil || len(rows) != 0 {
		t.Fatal("failed transaction leaked instance", e)
	}
}
