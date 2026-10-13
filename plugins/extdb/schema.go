package extdb

import (
	"context"
	"database/sql"
	"strings"
)

// Column is a single table column as reported by PRAGMA table_info.
type Column struct {
	Name    string  `json:"name"`
	Type    string  `json:"type"`
	NotNull bool    `json:"notNull"`
	Default *string `json:"default"`
	PK      bool    `json:"pk"`
}

// Table describes a table or view of an external database.
type Table struct {
	Name       string   `json:"name"`
	Type       string   `json:"type"` // "table" or "view"
	Columns    []Column `json:"columns"`
	PrimaryKey string   `json:"primaryKey"` // single pk column name or "rowid"; empty when records can't be addressed
	Editable   bool     `json:"editable"`
	Deletable  bool     `json:"deletable"`
}

func (t *Table) column(name string) *Column {
	for i := range t.Columns {
		if t.Columns[i].Name == name {
			return &t.Columns[i]
		}
	}
	return nil
}

func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func loadTables(ctx context.Context, db *sql.DB, cfg DatabaseConfig) ([]*Table, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT name, type, COALESCE(sql, '') FROM sqlite_master
		WHERE type IN ('table', 'view') AND name NOT LIKE 'sqlite_%'
		ORDER BY name`)
	if err != nil {
		return nil, err
	}

	type rawTable struct{ name, typ, sql string }
	var raws []rawTable
	for rows.Next() {
		var r rawTable
		if err := rows.Scan(&r.name, &r.typ, &r.sql); err != nil {
			rows.Close()
			return nil, err
		}
		raws = append(raws, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	tables := make([]*Table, 0, len(raws))
	for _, r := range raws {
		t := &Table{Name: r.name, Type: r.typ}

		cols, err := loadColumns(ctx, db, r.name)
		if err != nil {
			return nil, err
		}
		t.Columns = cols

		if r.typ == "table" {
			var pks []string
			for _, c := range cols {
				if c.PK {
					pks = append(pks, c.Name)
				}
			}

			withoutRowid := strings.Contains(strings.ToUpper(r.sql), "WITHOUT ROWID")
			switch {
			case len(pks) == 1:
				t.PrimaryKey = pks[0]
			case !withoutRowid:
				t.PrimaryKey = "rowid"
			}

			if t.PrimaryKey != "" {
				t.Editable = cfg.CanEdit(r.name)
				t.Deletable = cfg.CanDelete(r.name)
			}
		}

		tables = append(tables, t)
	}

	return tables, nil
}

func loadColumns(ctx context.Context, db *sql.DB, table string) ([]Column, error) {
	rows, err := db.QueryContext(ctx, "SELECT name, type, \"notnull\", dflt_value, pk FROM pragma_table_info(?)", table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var cols []Column
	for rows.Next() {
		var c Column
		var notNull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&c.Name, &c.Type, &notNull, &dflt, &pk); err != nil {
			return nil, err
		}
		c.NotNull = notNull == 1
		c.PK = pk > 0
		if dflt.Valid {
			c.Default = &dflt.String
		}
		cols = append(cols, c)
	}

	return cols, rows.Err()
}
