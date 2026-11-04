package storage

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ZZH1124Prime/ZaH/internal/domain"
)

func privateTemp(t *testing.T) string {
	t.Helper()
	p := t.TempDir()
	if err := os.Chmod(p, 0700); err != nil {
		t.Fatal(err)
	}
	return p
}
func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(privateTemp(t), "catalog.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func rec(kind, id, data string) domain.Record {
	return domain.Record{Kind: kind, ID: id, Data: json.RawMessage(data)}
}
func TestReferencesAreTransactional(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if err := s.Put(ctx, rec("artifact", "digest", `{"sha256":"digest"}`)); err != nil {
		t.Fatal(err)
	}
	r := rec("release", "release-1", `{"family_id":"family-1"}`)
	ref := domain.ResourceRef{FromKind: "release", FromID: r.ID, ToKind: "artifact", ToID: "digest", Role: "installer"}
	if err := s.PutWithReferences(ctx, r, []domain.ResourceRef{ref}); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, "artifact", "digest"); !errors.Is(err, domain.ErrReferenced) {
		t.Fatalf("wanted reference protection, got %v", err)
	}
	invalid := rec("release", "invalid", `{}`)
	ref.FromID = "invalid"
	ref.ToID = "missing"
	if err := s.PutWithReferences(ctx, invalid, []domain.ResourceRef{ref}); err == nil {
		t.Fatal("missing target accepted")
	}
	if _, err := s.Get(ctx, "release", "invalid"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("transaction leaked: %v", err)
	}
	if err := s.Delete(ctx, "release", "release-1"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, "artifact", "digest"); err != nil {
		t.Fatal(err)
	}
}
func TestImmutablePlanAndIdempotentJobs(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p := rec("plan", "plan-1", `{"id":"plan-1","operation":"import","steps":[]}`)
	if err := s.Put(ctx, p); err != nil {
		t.Fatal(err)
	}
	p.Data = json.RawMessage(`{"operation":"different"}`)
	if err := s.Put(ctx, p); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("plan changed: %v", err)
	}
	j, fresh, err := s.SubmitJob(ctx, p.ID, "click-1")
	if err != nil || !fresh {
		t.Fatalf("%+v %v %v", j, fresh, err)
	}
	dup, fresh, err := s.SubmitJob(ctx, p.ID, "click-1")
	if err != nil || fresh || dup.ID != j.ID {
		t.Fatalf("duplicate: %+v %v %v", dup, fresh, err)
	}
	recovered, fresh, err := s.SubmitJob(ctx, p.ID, "after-renderer-restart")
	if err != nil || fresh || recovered.ID != j.ID {
		t.Fatalf("one plan replayed with new key: %v %v %v", recovered, fresh, err)
	}
	if _, _, err = s.SubmitJob(ctx, "another-plan", "click-1"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("key reused: %v", err)
	}
	if err = s.TransitionJob(ctx, j.ID, domain.Queued, domain.Running, ""); err != nil {
		t.Fatal(err)
	}
	if err = s.SetWorker(ctx, j.ID, 123, "start-ticks"); err != nil {
		t.Fatal(err)
	}
	if err = s.TransitionJob(ctx, j.ID, domain.Running, domain.CancelRequested, ""); err != nil {
		t.Fatal(err)
	}
	if err = s.SetJobResult(ctx, j.ID, json.RawMessage(`{"copied":true}`)); err != nil {
		t.Fatal(err)
	}
	r, err := s.Get(ctx, "job", j.ID)
	if err != nil {
		t.Fatal(err)
	}
	json.Unmarshal(r.Data, &j)
	if j.State != domain.CancelRequested || j.WorkerPID != 123 || string(j.Result) != `{"copied":true}` {
		t.Fatalf("lost state: %+v", j)
	}
	if err = s.TransitionJob(ctx, j.ID, domain.Running, domain.Failed, ""); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("stale transition accepted: %v", err)
	}
	if err = s.TransitionJob(ctx, j.ID, domain.CancelRequested, domain.Succeeded, ""); err != nil {
		t.Fatal(err)
	}
	if err = s.TransitionJob(ctx, j.ID, domain.Succeeded, domain.Running, ""); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("terminal restarted: %v", err)
	}
	events, err := s.Events(ctx, 0, 100)
	if err != nil || len(events) < 5 {
		t.Fatalf("events: %d %v", len(events), err)
	}
	for i := 1; i < len(events); i++ {
		if events[i].Seq <= events[i-1].Seq {
			t.Fatal("event order")
		}
	}
}
func TestPersistenceAndFutureSchema(t *testing.T) {
	path := filepath.Join(privateTemp(t), "db.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err = s.Put(ctx, rec("family", "x", `{"name":"中文"}`)); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Get(ctx, "family", "x"); err != nil {
		t.Fatal(err)
	}
	var mode string
	if err = s.DB.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("mode=%s err=%v", mode, err)
	}
	if _, err = s.DB.Exec("PRAGMA user_version=999"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if s, err = Open(path); err == nil {
		s.Close()
		t.Fatal("future schema opened")
	}
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0600 {
		t.Fatalf("private file: %v %v", st, err)
	}
}
func TestRejectInvalidDataAndSymlinks(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	for _, r := range []domain.Record{rec("unknown", "x", `{}`), rec("family", "../x", `{}`), rec("family", "x", `[]`), rec("family", "x", `null`)} {
		if err := s.Put(ctx, r); err == nil {
			t.Fatalf("accepted %+v", r)
		}
	}
	dir := privateTemp(t)
	file := filepath.Join(dir, "target")
	os.WriteFile(file, nil, 0600)
	link := filepath.Join(dir, "link")
	os.Symlink(file, link)
	if other, err := Open(link); err == nil {
		other.Close()
		t.Fatal("symlink opened")
	}
}

func TestImportRecordsAtomic(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	records := []domain.Record{rec("family", "a", `{}`), rec("release", "b", `{}`)}
	refs := []domain.ResourceRef{{FromKind: "release", FromID: "b", ToKind: "family", ToID: "missing", Role: "family"}}
	if err := s.ImportRecords(ctx, records, refs); err == nil {
		t.Fatal("invalid refs accepted")
	}
	if _, err := s.Get(ctx, "family", "a"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("partial import: %v", err)
	}
	refs[0].ToID = "a"
	if err := s.ImportRecords(ctx, records, refs); err != nil {
		t.Fatal(err)
	}
	if err := s.ImportRecords(ctx, []domain.Record{rec("family", "new", `{}`), rec("family", "a", `{"changed":true}`)}, nil); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("collision: %v", err)
	}
	if _, err := s.Get(ctx, "family", "new"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("partial collision import")
	}
}

func TestConcurrentSubmitAcrossConnections(t *testing.T) {
	path := filepath.Join(privateTemp(t), "db.sqlite")
	a, e := Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	b, e := Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer b.Close()
	ctx := context.Background()
	if e = a.Put(ctx, rec("plan", "p", `{"id":"p","steps":[]}`)); e != nil {
		t.Fatal(e)
	}
	type outcome struct {
		id    string
		fresh bool
		err   error
	}
	results := make(chan outcome, 12)
	for i := 0; i < 12; i++ {
		s := a
		if i%2 == 1 {
			s = b
		}
		go func() { j, fresh, e := s.SubmitJob(ctx, "p", "shared-key"); results <- outcome{j.ID, fresh, e} }()
	}
	first := ""
	newCount := 0
	for i := 0; i < 12; i++ {
		r := <-results
		if r.err != nil {
			t.Fatal(r.err)
		}
		if first == "" {
			first = r.id
		}
		if first != r.id {
			t.Fatal("multiple jobs for same key")
		}
		if r.fresh {
			newCount++
		}
	}
	if newCount != 1 {
		t.Fatalf("new jobs %d", newCount)
	}
}

func TestUpdateRecordPreservesConcurrentFields(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if err := s.Put(ctx, rec("instance", "i", `{"favorite":false,"state":"ready"}`)); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 2)
	for key, value := range map[string]any{"favorite": true, "state": "running"} {
		go func(k string, v any) {
			done <- s.UpdateRecord(ctx, "instance", "i", func(r *domain.Record) error {
				var obj map[string]any
				if err := json.Unmarshal(r.Data, &obj); err != nil {
					return err
				}
				obj[k] = v
				var err error
				r.Data, err = json.Marshal(obj)
				return err
			})
		}(key, value)
	}
	for i := 0; i < 2; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	r, err := s.Get(ctx, "instance", "i")
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	json.Unmarshal(r.Data, &fields)
	if fields["favorite"] != true || fields["state"] != "running" {
		t.Fatalf("lost update %s", r.Data)
	}
	if err = s.UpdateRecord(ctx, "instance", "i", func(r *domain.Record) error { r.ID = "other"; return nil }); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("identity mutation: %v", err)
	}
}

func TestImportRetainsReusedMaterialSourcesAtomically(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if err := s.Put(ctx, rec("artifact", "digest", `{"sha256":"digest","source":{"original_name":"old"}}`)); err != nil {
		t.Fatal(err)
	}
	sources := map[string][]json.RawMessage{"digest": {json.RawMessage(`{"original_name":"new"}`)}}
	if err := s.ImportRecordsAndSources(ctx, []domain.Record{rec("family", "f", `{}`)}, []domain.ResourceRef{{FromKind: "family", FromID: "f", ToKind: "artifact", ToID: "missing", Role: "bad"}}, sources); err == nil {
		t.Fatal("missing ref accepted")
	}
	r, err := s.Get(ctx, "artifact", "digest")
	if err != nil {
		t.Fatal(err)
	}
	if string(r.Data) != `{"sha256":"digest","source":{"original_name":"old"}}` {
		t.Fatal("provenance changed after rollback")
	}
	if err = s.ImportRecordsAndSources(ctx, []domain.Record{rec("family", "f", `{}`)}, nil, sources); err != nil {
		t.Fatal(err)
	}
	r, err = s.Get(ctx, "artifact", "digest")
	if err != nil {
		t.Fatal(err)
	}
	var data struct {
		Sources []map[string]string `json:"sources"`
	}
	if err = json.Unmarshal(r.Data, &data); err != nil || len(data.Sources) != 2 {
		t.Fatalf("sources not retained %s %v", r.Data, err)
	}
}
