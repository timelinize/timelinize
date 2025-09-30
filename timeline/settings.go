package timeline

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"go.uber.org/zap"
)

func (tl *Timeline) GetProperties(ctx context.Context) (map[string]any, error) {
	properties := make(map[string]any)

	rows, err := tl.db.ReadPool.QueryContext(ctx, "SELECT key, value, type FROM repo")
	if err != nil {
		return nil, fmt.Errorf("querying properties: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var key string
		var valStr, valType *string
		err := rows.Scan(&key, &valStr, &valType)
		if err != nil {
			return nil, fmt.Errorf("scanning row: %w", err)
		}
		value, err := tl.readProperty(valStr, valType)
		if err != nil {
			return nil, fmt.Errorf("property %s: %w", key, err)
		}
		properties[key] = value
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scanning rows: %w", err)
	}

	return properties, nil
}

func (*Timeline) readProperty(valStr, valType *string) (any, error) {
	if valType == nil {
		return nil, errors.New("data type missing")
	}
	var value any
	var err error
	if valStr != nil && valType != nil {
		switch *valType {
		case "bool":
			switch strings.ToLower(*valStr) {
			case "0", "false", "", "no", "off", "disabled", "f":
				value = false
			case "1", "true", "yes", "on", "enabled", "t":
				value = true
			}
		case "int":
			value, err = strconv.ParseInt(*valStr, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("parsing property as %s: %w (value=%s)", *valType, err, *valStr)
			}
		case "float64":
			value, err = strconv.ParseFloat(*valStr, 64)
			if err != nil {
				return nil, fmt.Errorf("parsing property as %s: %w (value=%s)", *valType, err, *valStr)
			}
		case "string":
			value = *valStr
		}
	}
	return value, nil
}

func (tl *Timeline) LoadProperty(ctx context.Context, key string) (any, error) {
	var valStr, valType *string
	err := tl.db.ReadPool.QueryRowContext(ctx, "SELECT value, type FROM repo WHERE key=? LIMIT 1", key).Scan(&valStr, &valType)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("querying for property %s: %w", key, err)
	}
	value, err := tl.readProperty(valStr, valType)
	if err != nil {
		return nil, fmt.Errorf("property %s: %w", key, err)
	}
	return value, nil
}

func (tl *Timeline) GetProperty(ctx context.Context, key string) any {
	value, err := tl.LoadProperty(ctx, key)
	if err != nil {
		Log.Named("timeline").Error("getting property",
			zap.String("key", key),
			zap.Error(err))
		return nil
	}
	return value
}

func (tl *Timeline) SetProperties(ctx context.Context, properties map[string]any) error {
	tx, err := tl.db.WritePool.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("starting tx: %w", err)
	}
	defer tx.Rollback()

	for key, value := range properties {
		// can't change repo ID or version
		if key == "id" || key == "version" {
			continue
		}

		// nil values should just delete the row entirely
		if isNil(value) {
			_, err := tx.ExecContext(ctx, `DELETE FROM repo WHERE key=?`, key)
			if err != nil {
				return fmt.Errorf("deleting property %s: %w", key, err)
			}
			continue
		}

		// integers decoded from JSON will be float64 because ... JSON... so
		// try to convert them back to ints if possible
		if fl, ok := value.(float64); ok && float64(int(fl)) == fl {
			value = int(fl)
		}

		valType := fmt.Sprintf("%T", value) // commonly: int, bool, string, float64

		// to avoid ambiguity in the DB, store bools as true/false strings,
		// otherwise they just go in as 1/0 strings instead -- yes, we store
		// the type, but I think true/false is more intuitive
		if b, ok := value.(bool); ok {
			if b {
				value = "true"
			} else {
				value = "false"
			}
		}

		_, err := tx.ExecContext(ctx, `INSERT INTO repo (key, value, type) VALUES (?, ?, ?)
			ON CONFLICT DO UPDATE SET key=excluded.key, value=excluded.value, type=excluded.type`,
			key, value, valType)
		if err != nil {
			return fmt.Errorf("inserting/updating property %s=%v (%s): %w", key, value, valType, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing tx: %w", err)
	}

	return nil
}
