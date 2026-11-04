package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ZZH1124Prime/ZaH/internal/domain"
)

func TestResumeEventsPaginationAndEmptyPage(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	for n := 1; n <= 7; n++ {
		if err := s.Put(ctx, rec("family", fmt.Sprint(n), `{}`)); err != nil {
			t.Fatal(err)
		}
	}
	cursor := int64(0)
	for want := int64(1); want <= 7; {
		p, err := s.ResumeEvents(ctx, &cursor, 2)
		if err != nil {
			t.Fatal(err)
		}
		if p.Snapshot != nil || len(p.Events) == 0 || len(p.Events) > 2 {
			t.Fatalf("bad page: %+v", p)
		}
		for _, e := range p.Events {
			if e.Seq != want || e.Type != "record.changed" {
				t.Fatalf("missing event %d: %+v", want, e)
			}
			want++
		}
		if p.NextCursor != want-1 || p.HasMore != (want <= 7) {
			t.Fatalf("cursor skipped a page: %+v", p)
		}
		cursor = p.NextCursor
	}
	p, err := s.ResumeEvents(ctx, &cursor, 2)
	if err != nil || p.Events == nil || len(p.Events) != 0 || p.NextCursor != 7 || p.HasMore || p.Snapshot != nil {
		t.Fatalf("empty page: %+v %v", p, err)
	}
}

func TestResumeEventsSnapshotVisibility(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	input := []domain.Record{
		rec("instance", "visible", `{"environment_id":"used","state":"unprepared"}`),
		rec("instance", "hidden", `{"environment_id":"unused","state":"forgotten"}`),
		rec("environment", "used", `{}`), rec("environment", "unused", `{}`),
		rec("binding", "hidden", `{"state":"forgotten"}`), rec("capability", "hidden", `{"state":"forgotten"}`),
		rec("recipe", "recipe", `{}`), rec("plan", "plan", `{}`), rec("job", "job", `{}`),
	}
	for _, r := range input {
		if err := s.Put(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	p, err := s.ResumeEvents(ctx, nil, 100)
	if err != nil {
		t.Fatal(err)
	}
	if p.NextCursor != int64(len(input)) || p.HasMore || len(p.Events) != 0 || len(p.Snapshot) != 8 {
		t.Fatalf("snapshot: %+v", p)
	}
	for k, rs := range p.Snapshot {
		if rs == nil {
			t.Fatalf("null array %s", k)
		}
	}
	if len(p.Snapshot["instance"]) != 1 || p.Snapshot["instance"][0].ID != "visible" || len(p.Snapshot["environment"]) != 1 || p.Snapshot["environment"][0].ID != "used" || len(p.Snapshot["job"]) != 1 || len(p.Snapshot["binding"]) != 0 || len(p.Snapshot["capability"]) != 0 {
		t.Fatalf("visibility mismatch: %+v", p.Snapshot)
	}
	if _, ok := p.Snapshot["recipe"]; ok {
		t.Fatal("recipe included")
	}
	if _, ok := p.Snapshot["plan"]; ok {
		t.Fatal("plan included")
	}
	b, _ := json.Marshal(p)
	var wire map[string]json.RawMessage
	json.Unmarshal(b, &wire)
	if string(wire["events"]) != "[]" {
		t.Fatalf("events not array: %s", b)
	}
}

func TestResumeEventsRecoversHistoryGaps(t *testing.T) {
	for _, tc := range []struct {
		name, sql string
		after     int64
	}{
		{"ahead", "", 8}, {"middle", "DELETE FROM events WHERE seq=3", 0},
		{"leading", "DELETE FROM events WHERE seq=1", 0}, {"trailing", "DELETE FROM events WHERE seq>=4", 3},
		{"all", "DELETE FROM events", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := testStore(t)
			ctx := context.Background()
			for i := 0; i < 5; i++ {
				if err := s.Put(ctx, rec("family", fmt.Sprint(i), `{}`)); err != nil {
					t.Fatal(err)
				}
			}
			if tc.sql != "" {
				if _, err := s.DB.ExecContext(ctx, tc.sql); err != nil {
					t.Fatal(err)
				}
			}
			p, err := s.ResumeEvents(ctx, &tc.after, 2)
			if err != nil || p.Snapshot == nil || p.NextCursor != 5 || len(p.Events) != 0 || p.HasMore {
				t.Fatalf("did not reset: %+v %v", p, err)
			}
			cursor := p.NextCursor
			if err = s.Put(ctx, rec("family", "next", `{}`)); err != nil {
				t.Fatal(err)
			}
			p, err = s.ResumeEvents(ctx, &cursor, 2)
			if err != nil || p.Snapshot != nil || p.NextCursor != 6 || len(p.Events) != 1 {
				t.Fatalf("reset cursor cannot resume: %+v %v", p, err)
			}
		})
	}
}

func TestRecordEventsAndMutationsCommitTogether(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if err := s.Put(ctx, rec("family", "existing", `{"name":"before"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `CREATE TRIGGER reject_events BEFORE INSERT ON events BEGIN SELECT RAISE(ABORT,'event unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	for _, fn := range []func() error{
		func() error { return s.Put(ctx, rec("family", "new", `{}`)) },
		func() error {
			return s.UpdateRecord(ctx, "family", "existing", func(r *domain.Record) error { r.Data = json.RawMessage(`{"name":"after"}`); return nil })
		},
		func() error { return s.Delete(ctx, "family", "existing") },
	} {
		if err := fn(); err == nil {
			t.Fatal("mutation succeeded without event")
		}
	}
	if _, err := s.Get(ctx, "family", "new"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("failed put leaked: %v", err)
	}
	r, err := s.Get(ctx, "family", "existing")
	if err != nil || string(r.Data) != `{"name":"before"}` {
		t.Fatalf("mutation leaked: %+v %v", r, err)
	}
	if _, err = s.DB.ExecContext(ctx, `DROP TRIGGER reject_events`); err != nil {
		t.Fatal(err)
	}
	bad := domain.ResourceRef{FromKind: "release", FromID: "bad", ToKind: "family", ToID: "missing", Role: "family"}
	if err = s.PutWithReferences(ctx, rec("release", "bad", `{}`), []domain.ResourceRef{bad}); err == nil {
		t.Fatal("invalid reference accepted")
	}
	if err = s.UpdateRecord(ctx, "family", "existing", func(r *domain.Record) error { r.Data = json.RawMessage(`{"name":"after"}`); return nil }); err != nil {
		t.Fatal(err)
	}
	if err = s.Delete(ctx, "family", "existing"); err != nil {
		t.Fatal(err)
	}
	events, err := s.Events(ctx, 0, 100)
	if err != nil || len(events) != 3 {
		t.Fatalf("uncommitted events leaked: %+v %v", events, err)
	}
	for i, e := range events {
		var change struct {
			Kind, ID string
			Deleted  bool
		}
		if err = json.Unmarshal(e.Data, &change); err != nil {
			t.Fatal(err)
		}
		if change.Kind != "family" || change.ID != "existing" || change.Deleted != (i == 2) {
			t.Fatalf("bad notification: %s", e.Data)
		}
	}
}

func TestResumeEventsSnapshotConsistentWithConcurrentWriter(t *testing.T) {
	path := filepath.Join(privateTemp(t), "events.sqlite")
	writer, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	reader, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	ctx := context.Background()
	done := make(chan error, 1)
	go func() {
		for n := 1; n <= 60; n++ {
			if e := writer.Put(ctx, rec("family", "f", fmt.Sprintf(`{"generation":%d}`, n))); e != nil {
				done <- e
				return
			}
		}
		done <- nil
	}()
	for n := 0; n < 60; n++ {
		p, e := reader.ResumeEvents(ctx, nil, 1)
		if e != nil {
			t.Fatal(e)
		}
		generation := int64(0)
		if len(p.Snapshot["family"]) != 0 {
			var data struct{ Generation int64 }
			if e = json.Unmarshal(p.Snapshot["family"][0].Data, &data); e != nil {
				t.Fatal(e)
			}
			generation = data.Generation
		}
		if generation != p.NextCursor {
			t.Fatalf("snapshot and cursor differ: generation=%d cursor=%d", generation, p.NextCursor)
		}
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
}

func TestResumeEventsRejectsInvalidArguments(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	for _, v := range []int64{-1, maxEventCursor + 1} {
		if _, err := s.ResumeEvents(ctx, &v, 100); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("cursor %d: %v", v, err)
		}
	}
	for _, v := range []int{-1, 501} {
		if _, err := s.ResumeEvents(ctx, nil, v); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("limit %d: %v", v, err)
		}
	}
	p, err := s.ResumeEvents(ctx, nil, 0)
	if err != nil || p.NextCursor != 0 || p.Snapshot == nil || p.Events == nil {
		t.Fatalf("initial empty catalog: %+v %v", p, err)
	}
}

func TestResumeEventsLargeAuditBecomesBoundedNotification(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	payload, err := json.Marshal(map[string]any{"id": "job-large", "result": map[string]string{"catalog": strings.Repeat("x", 3<<20)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.ExecContext(ctx, `INSERT INTO events(type,data,created_at) VALUES('job.changed',?,?)`, string(payload), now()); err != nil {
		t.Fatal(err)
	}
	cursor := int64(0)
	page, err := s.ResumeEvents(ctx, &cursor, 500)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(page)
	if err != nil || len(encoded) >= 1<<20 || page.NextCursor != 1 || len(page.Events) != 1 || page.HasMore {
		t.Fatalf("large audit stalled cursor: bytes=%d page=%+v err=%v", len(encoded), page, err)
	}
	if string(page.Events[0].Data) != `{"kind":"job","id":"job-large"}` {
		t.Fatalf("leaked audit data: %s", page.Events[0].Data)
	}
	audit, err := s.Events(ctx, 0, 1)
	if err != nil || len(audit) != 1 || string(audit[0].Data) != string(payload) {
		t.Fatalf("audit changed: %v", err)
	}
}

func TestResumeEventsBoundedUnknownAndEscapedIdentities(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	// Maximum-length custom event types can expand sixfold when JSON escaped.
	// Unknown payloads, including fake kind/id hints, must never be forwarded.
	typ := strings.Repeat("\x01", 128)
	data, _ := json.Marshal(map[string]string{"kind": "family", "id": strings.Repeat("<", 256), "secret": "not for invalidation"})
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 500; n++ {
		if _, err = tx.ExecContext(ctx, `INSERT INTO events(type,data,created_at) VALUES(?,?,?)`, typ, string(data), now()); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	cursor := int64(0)
	page, err := s.ResumeEvents(ctx, &cursor, 500)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(page)
	if err != nil || len(encoded) >= 1<<20 || page.NextCursor != 500 || len(page.Events) != 500 {
		t.Fatalf("unbounded page: bytes=%d cursor=%d err=%v", len(encoded), page.NextCursor, err)
	}
	for _, event := range page.Events {
		if event.Type != typ || string(event.Data) != `{"kind":"unknown","id":""}` {
			t.Fatalf("arbitrary data forwarded: %s", event.Data)
		}
	}
	if got := eventNotification("record.changed", "family", strings.Repeat("<", 256)); string(got) != `{"kind":"family","id":""}` {
		t.Fatalf("unsafe identity hint: %s", got)
	}
}
