package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type TableRef struct {
	Schema string
	Name   string
	Kind   string
}

type SchemaNode struct {
	Name   string
	Tables []TableRef
}

type Column struct {
	Name     string
	Type     string
	Nullable bool
	Default  string
	PK       bool
}

type Index struct {
	Name string
	Def  string
}

type FK struct {
	Name string
	Def  string
}

type Cell struct {
	Text  string
	Title string
	Null  bool
}

type ResultSet struct {
	Columns []string
	Rows    [][]Cell
}

func quoteIdent(parts ...string) string {
	return pgx.Identifier(parts).Sanitize()
}

func listDatabases(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	rows, err := pool.Query(ctx, "SELECT datname FROM pg_database WHERE datallowconn AND NOT datistemplate ORDER BY datname")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

func listDBInfos(ctx context.Context, pool *pgxpool.Pool) ([]DBInfo, error) {
	rows, err := pool.Query(ctx, `
SELECT datname, pg_size_pretty(pg_database_size(datname))
FROM pg_database
WHERE datallowconn AND NOT datistemplate
ORDER BY datname`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DBInfo
	for rows.Next() {
		var d DBInfo
		if err := rows.Scan(&d.Name, &d.Size); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func loadTree(ctx context.Context, pool *pgxpool.Pool) ([]SchemaNode, error) {
	schemaRows, err := pool.Query(ctx, `
SELECT nspname FROM pg_namespace
WHERE nspname NOT IN ('pg_catalog', 'information_schema')
  AND nspname NOT LIKE 'pg_toast%' AND nspname NOT LIKE 'pg_temp%'
ORDER BY nspname`)
	if err != nil {
		return nil, err
	}
	defer schemaRows.Close()

	var nodes []SchemaNode
	index := map[string]int{}
	for schemaRows.Next() {
		var name string
		if err := schemaRows.Scan(&name); err != nil {
			return nil, err
		}
		index[name] = len(nodes)
		nodes = append(nodes, SchemaNode{Name: name})
	}
	if err := schemaRows.Err(); err != nil {
		return nil, err
	}

	rows, err := pool.Query(ctx, `
SELECT n.nspname, c.relname,
       CASE c.relkind WHEN 'v' THEN 'VIEW' WHEN 'm' THEN 'MATERIALIZED VIEW'
                      WHEN 'f' THEN 'FOREIGN TABLE' ELSE 'TABLE' END
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE c.relkind IN ('r', 'p', 'v', 'm', 'f')
  AND n.nspname NOT IN ('pg_catalog', 'information_schema')
  AND n.nspname NOT LIKE 'pg_toast%' AND n.nspname NOT LIKE 'pg_temp%'
ORDER BY n.nspname, c.relname`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var schema, name, kind string
		if err := rows.Scan(&schema, &name, &kind); err != nil {
			return nil, err
		}
		i, ok := index[schema]
		if !ok {
			index[schema] = len(nodes)
			nodes = append(nodes, SchemaNode{Name: schema})
			i = index[schema]
		}
		nodes[i].Tables = append(nodes[i].Tables, TableRef{Schema: schema, Name: name, Kind: kind})
	}
	return nodes, rows.Err()
}

func relationName(schema, table string) (string, error) {
	if table == "" || strings.ContainsRune(table, 0) || strings.ContainsRune(schema, 0) {
		return "", fmt.Errorf("表名无效")
	}
	return quoteIdent(schema, table), nil
}

func describeTable(ctx context.Context, pool *pgxpool.Pool, schema, table string) ([]Column, error) {
	rel, err := relationName(schema, table)
	if err != nil {
		return nil, err
	}
	sql := `
SELECT a.attname,
       format_type(a.atttypid, a.atttypmod),
       NOT a.attnotnull,
       COALESCE(pg_get_expr(d.adbin, d.adrelid), ''),
       EXISTS (SELECT 1 FROM pg_index i
               WHERE i.indrelid = a.attrelid AND i.indisprimary
                 AND a.attnum = ANY (i.indkey))
FROM pg_attribute a
LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
WHERE a.attrelid = '` + rel + `'::regclass
  AND a.attnum > 0 AND NOT a.attisdropped
ORDER BY a.attnum`

	rows, err := pool.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cols []Column
	for rows.Next() {
		var c Column
		if err := rows.Scan(&c.Name, &c.Type, &c.Nullable, &c.Default, &c.PK); err != nil {
			return nil, err
		}
		cols = append(cols, c)
	}
	return cols, rows.Err()
}

func tableIndexes(ctx context.Context, pool *pgxpool.Pool, schema, table string) ([]Index, error) {
	rows, err := pool.Query(ctx,
		"SELECT indexname, indexdef FROM pg_indexes WHERE schemaname = $1 AND tablename = $2 ORDER BY indexname",
		schema, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Index
	for rows.Next() {
		var i Index
		if err := rows.Scan(&i.Name, &i.Def); err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

func tableFKs(ctx context.Context, pool *pgxpool.Pool, schema, table string) ([]FK, error) {
	rel, err := relationName(schema, table)
	if err != nil {
		return nil, err
	}
	rows, err := pool.Query(ctx, `
SELECT conname, pg_get_constraintdef(oid)
FROM pg_constraint
WHERE conrelid = '`+rel+`'::regclass AND contype = 'f'
ORDER BY conname`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FK
	for rows.Next() {
		var f FK
		if err := rows.Scan(&f.Name, &f.Def); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func tableStats(ctx context.Context, pool *pgxpool.Pool, schema, table string) (size string, estimate int64, err error) {
	rel, err := relationName(schema, table)
	if err != nil {
		return "", 0, err
	}
	sql := "SELECT pg_size_pretty(pg_total_relation_size('" + rel + "'::regclass)), " +
		"(SELECT GREATEST(reltuples, 0)::bigint FROM pg_class WHERE oid = '" + rel + "'::regclass)"
	err = pool.QueryRow(ctx, sql).Scan(&size, &estimate)
	return size, estimate, err
}

func countRows(ctx context.Context, pool *pgxpool.Pool, schema, table, where string) (int64, error) {
	rel, err := relationName(schema, table)
	if err != nil {
		return 0, err
	}
	sql := "SELECT count(*) FROM " + rel
	if where != "" {
		sql += " WHERE " + where
	}
	var n int64
	err = pool.QueryRow(ctx, sql).Scan(&n)
	return n, err
}

func browseRows(ctx context.Context, pool *pgxpool.Pool, schema, table, where, sortCol, dir string, limit int, offset int64) ([][]Cell, error) {
	rel, err := relationName(schema, table)
	if err != nil {
		return nil, err
	}
	if dir != "desc" {
		dir = "asc"
	}
	sql := "SELECT * FROM " + rel
	if where != "" {
		sql += " WHERE " + where
	}
	sql += " ORDER BY " + quoteIdent(sortCol) + " " + dir +
		" LIMIT " + strconv.Itoa(limit) + " OFFSET " + strconv.FormatInt(offset, 10)
	_, rows, err := queryCells(ctx, pool, sql)
	return rows, err
}

// queryCells 以 simple protocol 执行查询,拿到 PostgreSQL 原始的文本输出(NULL/空串可区分)。
func queryCells(ctx context.Context, pool *pgxpool.Pool, sql string) ([]string, [][]Cell, error) {
	rows, err := pool.Query(ctx, sql, pgx.QueryExecModeSimpleProtocol)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	fds := rows.FieldDescriptions()
	cols := make([]string, len(fds))
	boolCol := make([]bool, len(fds))
	for i, fd := range fds {
		cols[i] = fd.Name
		boolCol[i] = fd.DataTypeOID == pgtype.BoolOID
	}

	var out [][]Cell
	for rows.Next() {
		raw := rows.RawValues()
		if raw == nil {
			// 非 simple protocol 时 RawValues 为 nil,改用 Values 兜底。
			vals, err := rows.Values()
			if err != nil {
				return cols, out, err
			}
			line := make([]Cell, len(vals))
			for i, v := range vals {
				if v == nil {
					line[i] = Cell{Null: true}
				} else {
					line[i] = newCell(fmt.Sprint(v), 300)
				}
			}
			out = append(out, line)
			continue
		}
		line := make([]Cell, len(raw))
		for i, b := range raw {
			if b == nil {
				line[i] = Cell{Null: true}
				continue
			}
			s := string(b)
			if boolCol[i] {
				if s == "t" {
					s = "true"
				} else if s == "f" {
					s = "false"
				}
			}
			line[i] = newCell(s, 300)
		}
		out = append(out, line)
	}
	return cols, out, rows.Err()
}

func newCell(s string, limit int) Cell {
	const maxTitle = 2000
	c := Cell{Text: s}
	if r := []rune(s); len(r) > limit {
		c.Text = string(r[:limit]) + "…"
		if len(r) > maxTitle {
			c.Title = string(r[:maxTitle]) + "…"
		} else {
			c.Title = s
		}
	}
	return c
}

// runSQL 通过 simple query 协议执行整段 SQL,支持一次提交多条语句并逐个取回结果集。
func runSQL(ctx context.Context, pool *pgxpool.Pool, sqlText string) ([]ResultSet, string, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, "", err
	}
	defer conn.Release()

	results, err := conn.Conn().PgConn().Exec(ctx, sqlText).ReadAll()
	if err != nil {
		return nil, "", err
	}

	var sets []ResultSet
	var tag string
	var stmtErr error
	for _, res := range results {
		if res.Err != nil {
			stmtErr = res.Err
			break
		}
		if len(res.FieldDescriptions) > 0 {
			rs := ResultSet{Columns: make([]string, len(res.FieldDescriptions))}
			for i, fd := range res.FieldDescriptions {
				rs.Columns[i] = fd.Name
			}
			for _, raw := range res.Rows {
				line := make([]Cell, len(raw))
				for i, b := range raw {
					if b == nil {
						line[i] = Cell{Null: true}
						continue
					}
					s := string(b)
					if res.FieldDescriptions[i].DataTypeOID == pgtype.BoolOID {
						if s == "t" {
							s = "true"
						} else if s == "f" {
							s = "false"
						}
					}
					line[i] = newCell(s, 300)
				}
				rs.Rows = append(rs.Rows, line)
			}
			sets = append(sets, rs)
		}
		if t := res.CommandTag.String(); t != "" {
			tag = t
		}
	}
	return sets, tag, stmtErr
}
