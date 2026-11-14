package service

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"tixgo/backend/internal/repository/sqlcgen"
)

// dynUpdate membangun UPDATE parameterized dengan kolom dari ALLOWLIST (nama kolom tidak pernah datang dari input).
// touch=true menambah updated_at=now(). Mengembalikan jumlah baris terpengaruh.
func dynUpdate(ctx context.Context, db sqlcgen.DBTX, table, idCol, idVal string, allowed map[string]bool, fields map[string]any, touch bool) (int64, error) {
	if len(fields) == 0 {
		return 0, nil
	}
	cols := make([]string, 0, len(fields))
	for c := range fields {
		if !allowed[c] {
			return 0, fmt.Errorf("kolom %q tidak diizinkan untuk %s", c, table)
		}
		cols = append(cols, c)
	}
	sort.Strings(cols)
	sets := make([]string, 0, len(cols)+1)
	args := make([]any, 0, len(cols)+1)
	for i, c := range cols {
		sets = append(sets, fmt.Sprintf("%s = $%d", c, i+1))
		args = append(args, fields[c])
	}
	if touch {
		sets = append(sets, "updated_at = now()")
	}
	args = append(args, idVal)
	q := fmt.Sprintf("UPDATE %s SET %s WHERE %s = $%d", table, strings.Join(sets, ", "), idCol, len(args))
	tag, err := db.Exec(ctx, q, args...)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
