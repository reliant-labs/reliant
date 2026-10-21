// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// The temporal_payload_blobs queries (P5b lane L2): the content-addressed
// claim-check store behind internal/temporal/claimcheck, which keeps large
// payloads out of Temporal histories.
package store

import (
	"context"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"
)

// PutTemporalPayloadBlob upserts a blob. Content-addressed, so a conflict
// means identical bytes: only the retention stamp is refreshed.
func PutTemporalPayloadBlob(ctx context.Context, db orm.Context, key string, data []byte, rawSize int) error {
	_, err := db.Bun().NewInsert().
		Model(&TemporalPayloadBlob{Key: key, Data: data, SizeBytes: int32(rawSize)}).
		On("CONFLICT (key) DO UPDATE").
		Set("last_referenced_at = NOW()").
		Returning("NULL").
		Exec(ctx)
	return err
}

// TemporalPayloadBlobData reads a blob's bytes. A miss is svcerr.NotFound.
// It deliberately leaves last_referenced_at alone: a read is on the
// workflow-task path, and the GC horizon already outlives every history that
// can reference the blob (claimcheck.DefaultGCHorizon).
func TemporalPayloadBlobData(ctx context.Context, db orm.Context, key string) ([]byte, error) {
	row, err := GetTemporalPayloadBlobByID(ctx, db, key)
	if err != nil {
		return nil, err
	}
	return row.Data, nil
}

// DeleteExpiredTemporalPayloadBlobs deletes up to batch blobs not referenced
// within horizon, returning how many it deleted. Callers loop until a batch
// comes back short, so a large backlog never holds one long lock.
func DeleteExpiredTemporalPayloadBlobs(ctx context.Context, db orm.Context, horizon time.Duration, batch int) (int64, error) {
	expired := db.Bun().NewSelect().Model((*TemporalPayloadBlob)(nil)).
		Column("key").
		Where("last_referenced_at < NOW() - make_interval(secs => ?)", horizon.Seconds()).
		Limit(int64(batch))
	res, err := db.Bun().NewDelete().Model((*TemporalPayloadBlob)(nil)).
		Where("key IN (?)", expired).
		Exec(ctx)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// TemporalPayloadBlobExtra carries fields on TemporalPayloadBlob that are NOT columns — computed
// values, transient state, anything derived rather than stored. TemporalPayloadBlob is
// embedded on TemporalPayloadBlobExtra (see temporal_payload_blob_orm.go), so a field added here is a field on
// every TemporalPayloadBlob value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type TemporalPayloadBlobExtra struct {
}
