package extdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	defaultPerPage = 30
	maxPerPage     = 500
)

// Filter is a single "column op value" condition.
type Filter struct {
	Column string `json:"column"`
	Op     string `json:"op"`
	Value  string `json:"value"`
}

// ListParams are the supported records list options.
type ListParams struct {
	Page    int
	PerPage int
	Sort    string // "col" or "-col"
	Search  string
	Filters []Filter
}

// ListResult mirrors the PocketBase records list response shape.
type ListResult struct {
	Page       int              `json:"page"`
	PerPage    int              `json:"perPage"`
	TotalItems int              `json:"totalItems"`
	TotalPages int              `json:"totalPages"`
	Items      []map[string]any `json:"items"`
}

var filterOps = map[string]string{
	"=":       "= ?",
	"!=":      "!= ?",
	">":       "> ?",
	">=":      ">= ?",
	"<":       "< ?",
	"<=":      "<= ?",
	"~":       "LIKE ?",
	"!~":      "NOT LIKE ?",
	"like":    "LIKE ?",
	"notlike": "NOT LIKE ?",
	"isnull":  "IS NULL",
	"notnull": "IS NOT NULL",
}

// pkKey is the extra item key holding the record identifier used by the get/update/delete endpoints.
const pkKey = "_pk"

func pkExpr(t *Table) string {
	if t.PrimaryKey == "rowid" {
		return "rowid"
	}
	return quoteIdent(t.PrimaryKey)
}

// selectColumns returns the select list. Date/time columns are read as raw TEXT so that the
// values round-trip unchanged on save (the driver would otherwise reformat them as time.Time).
func selectColumns(t *Table) string {
	parts := make([]string, 0, len(t.Columns)+1)
	if t.PrimaryKey != "" {
		parts = append(parts, pkExpr(t)+" AS "+quoteIdent(pkKey))
	}
	for _, c := range t.Columns {
		upper := strings.ToUpper(c.Type)
		if strings.Contains(upper, "DATE") || strings.Contains(upper, "TIME") {
			parts = append(parts, "CAST("+quoteIdent(c.Name)+" AS TEXT) AS "+quoteIdent(c.Name))
		} else {
			parts = append(parts, quoteIdent(c.Name))
		}
	}
	return strings.Join(parts, ", ")
}

func buildWhere(t *Table, params ListParams) (string, []any, error) {
	var conds []string
	var args []any

	for _, f := range params.Filters {
		if t.column(f.Column) == nil {
			return "", nil, fmt.Errorf("unknown filter column %q", f.Column)
		}
		op, ok := filterOps[f.Op]
		if !ok {
			return "", nil, fmt.Errorf("unsupported filter operator %q", f.Op)
		}

		conds = append(conds, quoteIdent(f.Column)+" "+op)
		switch f.Op {
		case "isnull", "notnull":
		case "~", "!~":
			args = append(args, "%"+f.Value+"%")
		default:
			args = append(args, f.Value)
		}
	}

	if search := strings.TrimSpace(params.Search); search != "" && len(t.Columns) > 0 {
		var ors []string
		for _, c := range t.Columns {
			ors = append(ors, "CAST("+quoteIdent(c.Name)+" AS TEXT) LIKE ?")
			args = append(args, "%"+search+"%")
		}
		conds = append(conds, "("+strings.Join(ors, " OR ")+")")
	}

	if len(conds) == 0 {
		return "", nil, nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args, nil
}

func buildOrder(t *Table, sort string) (string, error) {
	sort = strings.TrimSpace(sort)
	if sort == "" {
		if t.PrimaryKey != "" {
			return " ORDER BY " + pkExpr(t) + " DESC", nil
		}
		return "", nil
	}

	dir := "ASC"
	if strings.HasPrefix(sort, "-") {
		dir = "DESC"
		sort = sort[1:]
	}

	if sort == pkKey && t.PrimaryKey != "" {
		return " ORDER BY " + pkExpr(t) + " " + dir, nil
	}
	if t.column(sort) == nil {
		return "", fmt.Errorf("unknown sort column %q", sort)
	}
	return " ORDER BY " + quoteIdent(sort) + " " + dir, nil
}

func listRecords(ctx context.Context, db *sql.DB, t *Table, params ListParams) (*ListResult, error) {
	if params.Page < 1 {
		params.Page = 1
	}
	if params.PerPage <= 0 {
		params.PerPage = defaultPerPage
	}
	if params.PerPage > maxPerPage {
		params.PerPage = maxPerPage
	}

	where, args, err := buildWhere(t, params)
	if err != nil {
		return nil, err
	}
	order, err := buildOrder(t, params.Sort)
	if err != nil {
		return nil, err
	}

	result := &ListResult{Page: params.Page, PerPage: params.PerPage, Items: []map[string]any{}}

	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+quoteIdent(t.Name)+where, args...).Scan(&result.TotalItems); err != nil {
		return nil, err
	}
	result.TotalPages = (result.TotalItems + params.PerPage - 1) / params.PerPage

	query := "SELECT " + selectColumns(t) + " FROM " + quoteIdent(t.Name) + where + order + " LIMIT ? OFFSET ?"
	args = append(args, params.PerPage, (params.Page-1)*params.PerPage)

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result.Items, err = scanRows(rows)
	return result, err
}

// getRecord returns the record or (nil, nil) when it doesn't exist.
func getRecord(ctx context.Context, q queryer, t *Table, pk string) (map[string]any, error) {
	rows, err := q.QueryContext(ctx, "SELECT "+selectColumns(t)+" FROM "+quoteIdent(t.Name)+" WHERE "+pkExpr(t)+" = ? LIMIT 1", pkArg(t, pk))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items, err := scanRows(rows)
	if err != nil || len(items) == 0 {
		return nil, err
	}
	return items[0], nil
}

type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// updateRecord applies changes (column -> value, nil for NULL) and returns the record before and after.
func updateRecord(ctx context.Context, db *sql.DB, t *Table, pk string, changes map[string]any) (before, after map[string]any, err error) {
	if len(changes) == 0 {
		return nil, nil, errors.New("no changes submitted")
	}

	var sets []string
	var args []any
	for name, value := range changes {
		col := t.column(name)
		if col == nil {
			return nil, nil, fmt.Errorf("unknown column %q", name)
		}
		if col.PK {
			return nil, nil, fmt.Errorf("primary key column %q can't be changed", name)
		}
		if value == nil && col.NotNull {
			return nil, nil, fmt.Errorf("column %q can't be NULL", name)
		}
		v, err := normalizeValue(value)
		if err != nil {
			return nil, nil, fmt.Errorf("column %q: %w", name, err)
		}
		sets = append(sets, quoteIdent(name)+" = ?")
		args = append(args, v)
	}
	args = append(args, pkArg(t, pk))

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()

	before, err = getRecord(ctx, tx, t, pk)
	if err != nil {
		return nil, nil, err
	}
	if before == nil {
		return nil, nil, sql.ErrNoRows
	}

	if _, err = tx.ExecContext(ctx, "UPDATE "+quoteIdent(t.Name)+" SET "+strings.Join(sets, ", ")+" WHERE "+pkExpr(t)+" = ?", args...); err != nil {
		return nil, nil, err
	}

	after, err = getRecord(ctx, tx, t, pk)
	if err != nil {
		return nil, nil, err
	}

	return before, after, tx.Commit()
}

// deleteRecord deletes the record and returns its last state.
func deleteRecord(ctx context.Context, db *sql.DB, t *Table, pk string) (map[string]any, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	before, err := getRecord(ctx, tx, t, pk)
	if err != nil {
		return nil, err
	}
	if before == nil {
		return nil, sql.ErrNoRows
	}

	if _, err = tx.ExecContext(ctx, "DELETE FROM "+quoteIdent(t.Name)+" WHERE "+pkExpr(t)+" = ?", pkArg(t, pk)); err != nil {
		return nil, err
	}

	return before, tx.Commit()
}

// pkArg binds integer-like keys as int64 so they match INTEGER keys exactly.
func pkArg(t *Table, pk string) any {
	isInt := t.PrimaryKey == "rowid"
	if col := t.column(t.PrimaryKey); col != nil && strings.Contains(strings.ToUpper(col.Type), "INT") {
		isInt = true
	}
	if isInt {
		if n, err := strconv.ParseInt(pk, 10, 64); err == nil {
			return n
		}
	}
	return pk
}

func normalizeValue(v any) (any, error) {
	switch val := v.(type) {
	case nil, string, int64, float64:
		return val, nil
	case bool:
		if val {
			return int64(1), nil
		}
		return int64(0), nil
	case interface{ String() string }: // json.Number
		s := val.String()
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return n, nil
		}
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return f, nil
		}
		return s, nil
	default:
		return nil, fmt.Errorf("unsupported value type %T", v)
	}
}

func scanRows(rows *sql.Rows) ([]map[string]any, error) {
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}

	items := []map[string]any{}
	for rows.Next() {
		values := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}

		item := make(map[string]any, len(cols))
		for i, name := range cols {
			item[name] = jsonValue(values[i])
		}
		items = append(items, item)
	}

	return items, rows.Err()
}

func jsonValue(v any) any {
	if b, ok := v.([]byte); ok {
		if utf8.Valid(b) {
			return string(b)
		}
		return fmt.Sprintf("<binary %d bytes>", len(b))
	}
	return v
}
