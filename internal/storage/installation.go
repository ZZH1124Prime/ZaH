package storage

import (
	"bytes"
	"context"
	"github.com/ZZH1124Prime/ZaH/internal/domain"
)

// ReplaceInstallation atomically switches a verified entry and its environment
// proof, preserving every unrelated reference and rejecting concurrent changes.
func (s *Store) ReplaceInstallation(ctx context.Context, oldInstance, oldEnvironment, instance, environment, recipe domain.Record) error {
	if oldInstance.Kind != "instance" || oldEnvironment.Kind != "environment" || instance.Kind != "instance" || environment.Kind != "environment" || recipe.Kind != "recipe" || instance.ID != oldInstance.ID || environment.ID != oldEnvironment.ID {
		return domain.ErrInvalid
	}
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	for _, old := range []domain.Record{oldInstance, oldEnvironment} {
		current, e := get(ctx, tx, old.Kind, old.ID)
		if e != nil {
			return e
		}
		if !bytes.Equal(current.Data, old.Data) {
			return domain.ErrConflict
		}
	}
	for _, r := range []domain.Record{recipe, environment, instance} {
		if e = validate(r); e != nil {
			return e
		}
		if e = put(ctx, tx, r); e != nil {
			return e
		}
	}
	if _, e = tx.ExecContext(ctx, `DELETE FROM resource_refs WHERE from_kind='instance' AND from_id=? AND role='recipe'`, instance.ID); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO resource_refs(from_kind,from_id,to_kind,to_id,role) VALUES('instance',?,'recipe',?,'recipe')`, instance.ID, recipe.ID); e != nil {
		return e
	}
	return tx.Commit()
}
