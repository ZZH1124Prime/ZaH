package storage

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/ZZH1124Prime/ZaH/internal/domain"
	"testing"
)

func TestInstallationEntrySwitchAtomicCAS(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	oldI := rec("instance", "i", `{"id":"i","state":"ready","recipe_revision":"old"}`)
	oldE := rec("environment", "e", `{"id":"e","identity":"before"}`)
	for _, r := range []domain.Record{rec("recipe", "old", `{}`), oldI, oldE, rec("artifact", "a", `{}`)} {
		if e := s.Put(ctx, r); e != nil {
			t.Fatal(e)
		}
	}
	if e := s.PutWithReferences(ctx, oldI, []domain.ResourceRef{
		{FromKind: "instance", FromID: "i", ToKind: "recipe", ToID: "old", Role: "recipe"},
		{FromKind: "instance", FromID: "i", ToKind: "artifact", ToID: "a", Role: "original_artifact"},
	}); e != nil {
		t.Fatal(e)
	}
	nextI := oldI
	nextI.Data = json.RawMessage(`{"id":"i","state":"ready","recipe_revision":"new"}`)
	nextE := oldE
	nextE.Data = json.RawMessage(`{"id":"e","identity":"after"}`)
	newR := rec("recipe", "new", `{}`)
	stale := oldE
	stale.Data = json.RawMessage(`{"id":"e","identity":"stale"}`)
	if e := s.ReplaceInstallation(ctx, oldI, stale, nextI, nextE, newR); !errors.Is(e, domain.ErrConflict) {
		t.Fatal(e)
	}
	if _, e := s.Get(ctx, "recipe", "new"); !errors.Is(e, domain.ErrNotFound) {
		t.Fatal("partial recipe leaked", e)
	}
	if e := s.ReplaceInstallation(ctx, oldI, oldE, nextI, nextE, newR); e != nil {
		t.Fatal(e)
	}
	if e := s.Delete(ctx, "recipe", "old"); e != nil {
		t.Fatal(e)
	}
	if e := s.Delete(ctx, "recipe", "new"); !errors.Is(e, domain.ErrReferenced) {
		t.Fatal("lost new recipe reference", e)
	}
	if e := s.Delete(ctx, "artifact", "a"); !errors.Is(e, domain.ErrReferenced) {
		t.Fatal("lost original material reference", e)
	}
}
