package security

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Channel metadata is persisted and returned to administrators. Network errors
// must be scrubbed at this boundary just like logs, including old stored values.
func SafeChannelError(message string) string {
	if message == "" {
		return ""
	}
	return SafeLogError(errors.New(message))
}

// Clear previously persisted diagnostics without discarding connection labels.
// Compare the old value before updating so concurrent worker status is retained.
func ScrubStoredChannelErrors(ctx context.Context, db *pgxpool.Pool) error {
	rows, err := db.Query(ctx, `SELECT s.tenant_id,c.key,c.value->>'last_error' FROM app_settings s CROSS JOIN LATERAL jsonb_each(s.channel_metadata) c WHERE jsonb_typeof(c.value)='object' AND c.value->>'last_error' IS NOT NULL`)
	if err != nil {
		return err
	}
	type entry struct{ tenant, channel, old, safe string }
	var changes []entry
	for rows.Next() {
		var value entry
		if err := rows.Scan(&value.tenant, &value.channel, &value.old); err != nil {
			rows.Close()
			return err
		}
		value.safe = SafeChannelError(value.old)
		if value.safe != value.old {
			changes = append(changes, value)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, value := range changes {
		if _, err := db.Exec(ctx, `UPDATE app_settings SET channel_metadata=jsonb_set(channel_metadata,ARRAY[$1,'last_error'],to_jsonb($2::text)) WHERE tenant_id=$3 AND channel_metadata->$1->>'last_error'=$4`, value.channel, value.safe, value.tenant, value.old); err != nil {
			return err
		}
	}
	return nil
}
