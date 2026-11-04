package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/ZZH1124Prime/ZaH/internal/domain"
)

// Snapshot reads semantic catalog rows and references in one short transaction.
// It excludes plans and jobs, which must never be replayed on another machine.
func (s *Store) Snapshot(ctx context.Context) ([]domain.Record, []domain.ResourceRef, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	records, err := snapshotRecords(ctx, tx, []string{"family", "release", "instance", "environment", "binding", "capability", "recipe", "artifact"})
	if err != nil {
		return nil, nil, err
	}
	selected := map[string]bool{}
	for _, r := range records {
		selected[r.Kind+":"+r.ID] = true
	}
	rows, err := tx.QueryContext(ctx, `SELECT from_kind,from_id,to_kind,to_id,role FROM resource_refs ORDER BY from_kind,from_id,to_kind,to_id,role`)
	if err != nil {
		return nil, nil, err
	}
	refs := []domain.ResourceRef{}
	for rows.Next() {
		var r domain.ResourceRef
		if err = rows.Scan(&r.FromKind, &r.FromID, &r.ToKind, &r.ToID, &r.Role); err != nil {
			rows.Close()
			return nil, nil, err
		}
		if selected[r.FromKind+":"+r.FromID] && selected[r.ToKind+":"+r.ToID] {
			refs = append(refs, r)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, nil, err
	}
	return records, refs, nil
}

type rowsQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// snapshotRecords applies the same visibility rules to exports and event resumption.
func snapshotRecords(ctx context.Context, q rowsQueryer, kinds []string) ([]domain.Record, error) {
	records := []domain.Record{}
	for _, kind := range kinds {
		rows, e := q.QueryContext(ctx, `SELECT r.id,t.data,r.created_at,r.updated_at FROM records r JOIN `+tables[kind]+` t ON t.id=r.id WHERE r.kind=? AND NOT (r.kind IN ('instance','binding','capability') AND r.state='forgotten') AND (r.kind!='environment' OR EXISTS(SELECT 1 FROM app_instances i WHERE json_extract(i.data,'$.environment_id')=r.id AND json_extract(i.data,'$.state')!='forgotten')) ORDER BY r.id`, kind)
		if e != nil {
			return nil, e
		}
		for rows.Next() {
			var r domain.Record
			var data, created, updated string
			r.Kind = kind
			if e = rows.Scan(&r.ID, &data, &created, &updated); e != nil {
				rows.Close()
				return nil, e
			}
			r.Data = json.RawMessage(data)
			r.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
			r.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
			records = append(records, r)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return nil, e
		}
	}
	return records, nil
}
