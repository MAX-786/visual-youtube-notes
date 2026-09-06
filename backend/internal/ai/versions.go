package ai

import (
	"context"
	"fmt"

	"github.com/MAX-786/visual-youtube-notes/backend/internal/keys"
)

// CheckKeyVersions refuses to boot when a provider key was sealed under a
// master key version the file no longer holds (ADR 0010). Booting anyway
// would turn a configuration problem into a per-Note decrypt error at
// generating, where it reads like a provider fault. Both the API and the
// worker run it.
func CheckKeyVersions(ctx context.Context, q querier, ring *keys.Ring) error {
	rows, err := q.Query(ctx, `SELECT DISTINCT key_version FROM ai_role_configs WHERE key_version IS NOT NULL`)
	if err != nil {
		return fmt.Errorf("read key versions: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil {
			return err
		}
		if _, ok := ring.ByVersion(version); !ok {
			return fmt.Errorf("provider keys are sealed under master key version %d, which the key file does not hold; restore it before starting", version)
		}
	}
	return rows.Err()
}
