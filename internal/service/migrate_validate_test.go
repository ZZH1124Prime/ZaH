package service

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ZZH1124Prime/ZaH/internal/catalog"
	"github.com/ZZH1124Prime/ZaH/internal/domain"
	"github.com/ZZH1124Prime/ZaH/internal/recipe"
)

func TestCatalogSemanticValidation(t *testing.T) {
	digest := strings.Repeat("a", 64)
	recipeValue := recipe.Recipe{SchemaVersion: 1, ID: "sample", Revision: "1", Platform: "linux", Architecture: "amd64", Strategy: "appimage", Image: "docker.io/library/ubuntu@sha256:" + digest, Entry: "squashfs-root/AppRun"}
	baseline := catalogSnapshot{Format: catalogBundleFormat, SchemaVersion: 1, Records: []domain.Record{
		record("family", "f", domain.AppFamily{ID: "f", Name: "Family"}),
		record("release", "r", domain.AppRelease{ID: "r", FamilyID: "f", Platform: "linux", Arch: "amd64", Artifacts: []string{digest}}),
		record("artifact", digest, catalog.Artifact{SHA256: digest, Size: 1}),
		record("recipe", recipeValue.Digest(), recipeValue),
		record("environment", "e", domain.Environment{ID: "e", Backend: "distrobox-docker"}),
		record("instance", "i", domain.AppInstance{ID: "i", Name: "instance", ReleaseID: "r", EnvironmentID: "e", RecipeRevision: recipeValue.Digest(), OriginalArtifact: digest}),
	}}
	if err := validateCatalogSnapshot(baseline); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		mutate func(*catalogSnapshot)
	}{
		{"empty materials", func(s *catalogSnapshot) {
			s.Records[1] = record("release", "r", domain.AppRelease{ID: "r", FamilyID: "f", Platform: "linux", Arch: "amd64"})
		}},
		{"mismatched identity", func(s *catalogSnapshot) {
			s.Records[0] = record("family", "f", domain.AppFamily{ID: "other", Name: "Family"})
		}},
		{"unknown backend", func(s *catalogSnapshot) {
			s.Records[4] = record("environment", "e", domain.Environment{ID: "e", Backend: "arbitrary"})
		}},
		{"wrong backend", func(s *catalogSnapshot) {
			s.Records[4] = record("environment", "e", domain.Environment{ID: "e", Backend: "bottles-flatpak"})
		}},
		{"non-object", func(s *catalogSnapshot) { s.Records[0].Data = json.RawMessage(`null`) }},
		{"dangling ref", func(s *catalogSnapshot) {
			s.References = []domain.ResourceRef{{FromKind: "family", FromID: "f", ToKind: "instance", ToID: "missing", Role: "default"}}
		}},
		{"shared environment", func(s *catalogSnapshot) {
			v := domain.AppInstance{ID: "i2", Name: "second", ReleaseID: "r", EnvironmentID: "e", RecipeRevision: recipeValue.Digest(), OriginalArtifact: digest}
			s.Records = append(s.Records, record("instance", "i2", v))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			copyOf := baseline
			copyOf.Records = append([]domain.Record(nil), baseline.Records...)
			tc.mutate(&copyOf)
			if err := validateCatalogSnapshot(copyOf); err == nil {
				t.Fatal("malformed semantic snapshot accepted")
			}
		})
	}
}

func TestBindingTargetPlatformRules(t *testing.T) {
	for _, tc := range []struct {
		platform, target string
		valid            bool
	}{
		{"linux", "/mnt/zah/music", true}, {"linux", "/music", false}, {"linux", "M:", false}, {"windows", "M:", true}, {"windows", "D:", true}, {"windows", "Y:", true}, {"windows", "C:", false}, {"windows", "Z:", false}, {"windows", "m:", false}, {"windows", "/mnt/zah/music", false},
	} {
		if got := validBindingTarget(tc.platform, tc.target); got != tc.valid {
			t.Fatalf("%+v got %v", tc, got)
		}
	}
}
func TestWindowsDriveSurvivesSemanticCatalogValidation(t *testing.T) {
	digest := strings.Repeat("b", 64)
	recipeValue := recipe.Recipe{SchemaVersion: 1, ID: "win", Revision: "1", Platform: "windows", Architecture: "amd64", Strategy: "exe", Runner: "wine-test", Entry: "app.exe"}
	// Use the documented Windows strategy and a stable program name.
	baseline := catalogSnapshot{Format: catalogBundleFormat, SchemaVersion: 1, Records: []domain.Record{
		record("family", "f", domain.AppFamily{ID: "f", Name: "Win"}),
		record("release", "r", domain.AppRelease{ID: "r", FamilyID: "f", Platform: "windows", Arch: "amd64", Artifacts: []string{digest}}),
		record("artifact", digest, catalog.Artifact{SHA256: digest, Size: 1}),
		record("recipe", recipeValue.Digest(), recipeValue),
		record("environment", "e", domain.Environment{ID: "e", Backend: "bottles-flatpak"}),
		record("instance", "i", domain.AppInstance{ID: "i", Name: "instance", ReleaseID: "r", EnvironmentID: "e", RecipeRevision: recipeValue.Digest(), OriginalArtifact: digest}),
		record("binding", "b", domain.FileBinding{ID: "b", InstanceID: "i", TargetPath: "M:", Access: "read"}),
	}}
	if e := validateCatalogSnapshot(baseline); e != nil {
		t.Fatal(e)
	}
	baseline.Records[len(baseline.Records)-1] = record("binding", "b", domain.FileBinding{ID: "b", InstanceID: "i", TargetPath: "C:", Access: "read"})
	if e := validateCatalogSnapshot(baseline); e == nil {
		t.Fatal("restored reserved Windows drive")
	}
}
