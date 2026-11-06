package main

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
)

// dedupeBlockReason names why a twin group cannot be collapsed safely.
// The values are part of the dedupe output contract.
type dedupeBlockReason string

const (
	dedupeBlockContentDiffers          dedupeBlockReason = "content_differs"
	dedupeBlockDuplicateHasRevisions   dedupeBlockReason = "duplicate_has_revisions"
	dedupeBlockDuplicateHasAttachments dedupeBlockReason = "duplicate_has_attachments"
)

type dedupeBlock struct {
	DuplicateID int64             `json:"duplicate_id"`
	Reason      dedupeBlockReason `json:"reason"`
}

// dedupeTwinGroup is every Data Point sharing one logical identity under
// the Health Archive Snapshot validator's rule. KeepID is the lowest id.
// Any block leaves the whole group untouched.
type dedupeTwinGroup struct {
	KeepID       int64
	DuplicateIDs []int64
	DataType     string
	Blocks       []dedupeBlock
}

func (group dedupeTwinGroup) removable() bool {
	return len(group.Blocks) == 0
}

type dedupePlan struct {
	Groups []dedupeTwinGroup
}

func (plan dedupePlan) removableIDs() []int64 {
	var ids []int64
	for _, group := range plan.Groups {
		if group.removable() {
			ids = append(ids, group.DuplicateIDs...)
		}
	}
	return ids
}

// planDataPointDedupe groups Data Points by dataPointIdentityKey so a
// group exists exactly when ValidateHealthArchiveSnapshot would reject
// the archive for a duplicate Data Point identity.
func planDataPointDedupe(ctx context.Context, db healthArchiveSnapshotQuerier) (dedupePlan, error) {
	points, err := exportSnapshotDataPoints(ctx, db)
	if err != nil {
		return dedupePlan{}, err
	}
	withRevisions, err := dataPointIDSet(ctx, db, `SELECT DISTINCT data_point_id FROM data_point_revisions`)
	if err != nil {
		return dedupePlan{}, err
	}
	withAttachments, err := dataPointIDSet(ctx, db, `SELECT DISTINCT data_point_id FROM data_point_attachments`)
	if err != nil {
		return dedupePlan{}, err
	}

	var order []string
	byIdentity := map[string][]HealthArchiveSnapshotDataPoint{}
	for _, point := range points {
		identity := dataPointIdentityKey(point)
		if _, seen := byIdentity[identity]; !seen {
			order = append(order, identity)
		}
		byIdentity[identity] = append(byIdentity[identity], point)
	}

	var plan dedupePlan
	for _, identity := range order {
		members := byIdentity[identity]
		if len(members) < 2 {
			continue
		}
		keeper := members[0]
		group := dedupeTwinGroup{KeepID: keeper.ID, DataType: keeper.DataType}
		for _, duplicate := range members[1:] {
			group.DuplicateIDs = append(group.DuplicateIDs, duplicate.ID)
			if !sameDataPointContent(keeper, duplicate) {
				group.Blocks = append(group.Blocks, dedupeBlock{duplicate.ID, dedupeBlockContentDiffers})
			}
			if withRevisions[duplicate.ID] {
				group.Blocks = append(group.Blocks, dedupeBlock{duplicate.ID, dedupeBlockDuplicateHasRevisions})
			}
			if withAttachments[duplicate.ID] {
				group.Blocks = append(group.Blocks, dedupeBlock{duplicate.ID, dedupeBlockDuplicateHasAttachments})
			}
		}
		plan.Groups = append(plan.Groups, group)
	}
	return plan, nil
}

// sameDataPointContent compares every archived column except the row's
// own id and insert/update stamps; raw_json is compared byte for byte.
func sameDataPointContent(a, b HealthArchiveSnapshotDataPoint) bool {
	a.ID, a.InsertedAt, a.UpdatedAt = 0, "", ""
	b.ID, b.InsertedAt, b.UpdatedAt = 0, "", ""
	return reflect.DeepEqual(a, b)
}

func dataPointIDSet(ctx context.Context, db healthArchiveSnapshotQuerier, query string) (map[int64]bool, error) {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids[id] = true
	}
	return ids, rows.Err()
}

// inspectDataPointDedupe plans without writing: the inspection-only
// handle never migrates the schema or repairs the Attachment root.
func inspectDataPointDedupe(ctx context.Context, archivePath string) (plan dedupePlan, err error) {
	handle, err := (healthArchiveLifecycle{path: archivePath}).openInspectionOnly(ctx)
	if err != nil {
		return dedupePlan{}, err
	}
	defer func() { err = errors.Join(err, handle.Close()) }()
	tx, err := handle.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return dedupePlan{}, err
	}
	defer func() { _ = tx.Rollback() }()
	return planDataPointDedupe(ctx, tx)
}

// applyDataPointDedupe first runs the dry run's inspection-only open, so
// both modes refuse the same stale or WAL archives, then opens the
// writable DSN directly: no migration and no Attachment-root repair.
// The plan is recomputed inside one BEGIN IMMEDIATE transaction, so a
// concurrent sync cannot change a group between planning and deletion.
func applyDataPointDedupe(ctx context.Context, archivePath string) (plan dedupePlan, removed int, err error) {
	inspection, err := (healthArchiveLifecycle{path: archivePath}).openInspectionOnly(ctx)
	if err != nil {
		return dedupePlan{}, 0, err
	}
	if err := inspection.Close(); err != nil {
		return dedupePlan{}, 0, err
	}
	db, err := openArchive(archivePath)
	if err != nil {
		return dedupePlan{}, 0, err
	}
	defer func() { err = errors.Join(err, db.Close()) }()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return dedupePlan{}, 0, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	plan, err = planDataPointDedupe(ctx, tx)
	if err != nil {
		return dedupePlan{}, 0, err
	}
	for _, id := range plan.removableIDs() {
		result, err := tx.ExecContext(ctx, `DELETE FROM data_points WHERE id = ?`, id)
		if err != nil {
			return dedupePlan{}, 0, err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return dedupePlan{}, 0, err
		}
		removed += int(affected)
	}
	if err := tx.Commit(); err != nil {
		return dedupePlan{}, 0, err
	}
	committed = true
	return plan, removed, nil
}
