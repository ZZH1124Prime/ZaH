package storage

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ZZH1124Prime/ZaH/internal/domain"
)

// ForgetInstance removes live catalog references in one transaction while
// retaining instance, binding, capability, plan and job records for audit.
// The service must first prove runtime inactivity and hold modification/env locks.
func (s *Store) ForgetInstance(ctx context.Context, id, expectedState string) error {
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	r, e := get(ctx, tx, "instance", id)
	if e != nil {
		return e
	}
	var instance domain.AppInstance
	if e = json.Unmarshal(r.Data, &instance); e != nil {
		return e
	}
	if instance.State == "forgotten" {
		return nil
	}
	if instance.State != expectedState {
		return domain.ErrConflict
	}
	instance.State = "forgotten"
	instance.Favorite = false
	instance.Entry = nil
	r.Data, e = json.Marshal(instance)
	if e != nil {
		return e
	}
	if e = put(ctx, tx, r); e != nil {
		return e
	}
	rows, e := tx.QueryContext(ctx, `SELECT id,data FROM app_families WHERE json_extract(data,'$.default_instance_id')=?`, id)
	if e != nil {
		return e
	}
	families := []domain.AppFamily{}
	for rows.Next() {
		var key, raw string
		if e = rows.Scan(&key, &raw); e != nil {
			rows.Close()
			return e
		}
		var f domain.AppFamily
		if e = json.Unmarshal([]byte(raw), &f); e != nil {
			rows.Close()
			return e
		}
		if f.ID != key {
			rows.Close()
			return fmt.Errorf("%w: family identity mismatch", domain.ErrInvalid)
		}
		families = append(families, f)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, f := range families {
		f.DefaultInstanceID = ""
		data, e := json.Marshal(f)
		if e != nil {
			return e
		}
		if e = put(ctx, tx, domain.Record{Kind: "family", ID: f.ID, Data: data}); e != nil {
			return e
		}
	}
	for _, kind := range []string{"binding", "capability"} {
		rows, e = tx.QueryContext(ctx, `SELECT id,data FROM `+tables[kind]+` WHERE json_extract(data,'$.instance_id')=?`, id)
		if e != nil {
			return e
		}
		children := []domain.Record{}
		for rows.Next() {
			var key, raw string
			if e = rows.Scan(&key, &raw); e != nil {
				rows.Close()
				return e
			}
			var data map[string]json.RawMessage
			if e = json.Unmarshal([]byte(raw), &data); e != nil {
				rows.Close()
				return e
			}
			data["state"] = json.RawMessage(`"forgotten"`)
			encoded, e := json.Marshal(data)
			if e != nil {
				rows.Close()
				return e
			}
			children = append(children, domain.Record{Kind: kind, ID: key, Data: encoded})
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		for _, child := range children {
			if e = put(ctx, tx, child); e != nil {
				return e
			}
			if _, e = tx.ExecContext(ctx, `DELETE FROM resource_refs WHERE from_kind=? AND from_id=?`, kind, child.ID); e != nil {
				return e
			}
		}
	}
	if _, e = tx.ExecContext(ctx, `DELETE FROM resource_refs WHERE (from_kind='instance' AND from_id=?) OR (from_kind='family' AND to_kind='instance' AND to_id=?)`, id, id); e != nil {
		return e
	}
	event, _ := json.Marshal(map[string]string{"id": id, "state": "forgotten", "private_data": "preserved"})
	if _, e = tx.ExecContext(ctx, `INSERT INTO events(type,data,created_at) VALUES('instance.forgotten',?,?)`, string(event), now()); e != nil {
		return e
	}
	return tx.Commit()
}
