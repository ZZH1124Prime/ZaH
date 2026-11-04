package storage

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/ZZH1124Prime/ZaH/internal/domain"
)

func TestReplaceReferenceRoleIsAtomicAndPreservesOtherRoles(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	for _, id := range []string{"a", "b", "material"} {
		if err := s.Put(ctx, rec("artifact", id, `{}`)); err != nil {
			t.Fatal(err)
		}
	}
	owner := rec("release", "r", `{"icon":"a"}`)
	refs := []domain.ResourceRef{{FromKind: "release", FromID: "r", ToKind: "artifact", ToID: "a", Role: "icon"}, {FromKind: "release", FromID: "r", ToKind: "artifact", ToID: "material", Role: "material"}}
	if err := s.PutWithReferences(ctx, owner, refs); err != nil {
		t.Fatal(err)
	}
	owner.Data = json.RawMessage(`{"icon":"b"}`)
	replacement := []domain.ResourceRef{{FromKind: "release", FromID: "r", ToKind: "artifact", ToID: "missing", Role: "icon"}}
	if err := s.PutReplacingReferenceRole(ctx, owner, "icon", replacement); err == nil {
		t.Fatal("missing dependency accepted")
	}
	previous, err := s.Get(ctx, "release", "r")
	if err != nil || string(previous.Data) != `{"icon":"a"}` {
		t.Fatalf("failed change leaked: %+v %v", previous, err)
	}
	if err = s.Delete(ctx, "artifact", "a"); !errors.Is(err, domain.ErrReferenced) {
		t.Fatalf("old reference lost: %v", err)
	}
	replacement[0].ToID = "b"
	if err = s.PutReplacingReferenceRole(ctx, owner, "icon", replacement); err != nil {
		t.Fatal(err)
	}
	if err = s.Delete(ctx, "artifact", "a"); err != nil {
		t.Fatalf("old icon still retained: %v", err)
	}
	for _, id := range []string{"b", "material"} {
		if err = s.Delete(ctx, "artifact", id); !errors.Is(err, domain.ErrReferenced) {
			t.Fatalf("lost %s dependency: %v", id, err)
		}
	}
}

func TestRecoveredMaterialsCASAndBatchRollback(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	old := rec("artifact", "a", `{"availability":"removed","source":{"original_name":"original"}}`)
	if err := s.Put(ctx, old); err != nil {
		t.Fatal(err)
	}
	incoming := []domain.Record{rec("family", "new", `{}`)}
	if err := s.ImportRecordsAndRecoveredMaterials(ctx, incoming, nil, nil, map[string]json.RawMessage{"a": json.RawMessage(`{"availability":"removed"}`)}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("changed tombstone accepted: %v", err)
	}
	if _, err := s.Get(ctx, "family", "new"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("partial records committed", err)
	}
	badRefs := []domain.ResourceRef{{FromKind: "family", FromID: "new", ToKind: "artifact", ToID: "missing", Role: "material"}}
	if err := s.ImportRecordsAndRecoveredMaterials(ctx, incoming, badRefs, nil, map[string]json.RawMessage{"a": old.Data}); err == nil {
		t.Fatal("bad references accepted")
	}
	got, err := s.Get(ctx, "artifact", "a")
	if err != nil || string(got.Data) != string(old.Data) {
		t.Fatal("tombstone changed on rollback", err)
	}
	if err = s.ImportRecordsAndRecoveredMaterials(ctx, incoming, nil, nil, map[string]json.RawMessage{"a": old.Data}); err != nil {
		t.Fatal(err)
	}
	got, err = s.Get(ctx, "artifact", "a")
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	json.Unmarshal(got.Data, &fields)
	if string(fields["availability"]) != `"available"` || string(fields["source"]) != `{"original_name":"original"}` {
		t.Fatalf("recovery overwrote facts: %s", got.Data)
	}
}
