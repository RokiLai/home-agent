package mysqlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"homeagent/internal/store"
)

func (s *MySQLStore) LoadControlPlane(ctx context.Context) (*store.ControlPlaneSnapshot, error) {
	var data []byte
	err := s.db.QueryRowContext(ctx, `SELECT snapshot_json FROM control_plane_snapshots WHERE id = 1`).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return store.NewControlPlaneSnapshot(), nil
	}
	if err != nil {
		return nil, err
	}
	return decodeControlPlane(data)
}

func (s *MySQLStore) CommitControlPlane(ctx context.Context, expectedRevision uint64, next *store.ControlPlaneSnapshot) (*store.ControlPlaneSnapshot, error) {
	transaction, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return nil, err
	}
	defer transaction.Rollback()
	var currentRevision uint64
	err = transaction.QueryRowContext(ctx, `SELECT revision FROM control_plane_snapshots WHERE id = 1 FOR UPDATE`).Scan(&currentRevision)
	if errors.Is(err, sql.ErrNoRows) {
		currentRevision = 0
	} else if err != nil {
		return nil, err
	}
	if currentRevision != expectedRevision {
		return nil, store.ErrRevisionConflict
	}
	candidate, err := cloneControlPlane(next)
	if err != nil {
		return nil, err
	}
	candidate.Revision = expectedRevision + 1
	if err := store.ValidateControlPlaneSnapshot(candidate); err != nil {
		return nil, err
	}
	data, err := json.Marshal(candidate)
	if err != nil {
		return nil, err
	}
	_, err = transaction.ExecContext(ctx, `INSERT INTO control_plane_snapshots (id, revision, snapshot_json, updated_at) VALUES (1, ?, ?, ?) ON DUPLICATE KEY UPDATE revision = VALUES(revision), snapshot_json = VALUES(snapshot_json), updated_at = VALUES(updated_at)`, candidate.Revision, data, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	if err := transaction.Commit(); err != nil {
		return nil, err
	}
	return candidate, nil
}

type mysqlControlPlaneRepository struct{ store *MySQLStore }

func (s *MySQLStore) ControlPlaneRepository() store.ControlPlaneRepository {
	return &mysqlControlPlaneRepository{store: s}
}

func (repository *mysqlControlPlaneRepository) Load(ctx context.Context) (*store.ControlPlaneSnapshot, error) {
	return repository.store.LoadControlPlane(ctx)
}

func (repository *mysqlControlPlaneRepository) Commit(ctx context.Context, expectedRevision uint64, next *store.ControlPlaneSnapshot) (*store.ControlPlaneSnapshot, error) {
	return repository.store.CommitControlPlane(ctx, expectedRevision, next)
}

func decodeControlPlane(data []byte) (*store.ControlPlaneSnapshot, error) {
	var snapshot store.ControlPlaneSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return nil, err
	}
	if err := store.ValidateControlPlaneSnapshot(&snapshot); err != nil {
		return nil, err
	}
	return &snapshot, nil
}

func cloneControlPlane(snapshot *store.ControlPlaneSnapshot) (*store.ControlPlaneSnapshot, error) {
	data, err := json.Marshal(snapshot)
	if err != nil {
		return nil, err
	}
	return decodeControlPlane(data)
}
