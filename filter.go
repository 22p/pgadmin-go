package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// Filter 表示浏览页的一个筛选条件。
type Filter struct {
	Column string
	Op     string
	Value  string
}

type FilterOption struct {
	Value string
	Label string
}

// FilterChip 是工具栏上展示的单个筛选条件。
type FilterChip struct {
	Label     string
	RemoveURL string
}

var filterOptions = []FilterOption{
	{"=", "="},
	{"!=", "≠"},
	{">", ">"},
	{">=", "≥"},
	{"<", "<"},
	{"<=", "≤"},
	{"contains", "包含"},
	{"starts", "开头是"},
	{"like", "LIKE"},
	{"isnull", "为空"},
	{"notnull", "不为空"},
}

func validFilterOp(op string) bool {
	for _, o := range filterOptions {
		if o.Value == op {
			return true
		}
	}
	return false
}

func hasColumn(cols []Column, name string) bool {
	for _, c := range cols {
		if c.Name == name {
			return true
		}
	}
	return false
}

// parseFilters 读取 f.col / f.op / f.val 三组参数,丢弃非法的列与操作符。
func parseFilters(q url.Values, cols []Column) []Filter {
	names, ops, vals := q["f.col"], q["f.op"], q["f.val"]
	var out []Filter
	for i, name := range names {
		if !hasColumn(cols, name) {
			continue
		}
		op := "="
		if i < len(ops) && validFilterOp(ops[i]) {
			op = ops[i]
		}
		val := ""
		if i < len(vals) {
			val = strings.TrimSpace(vals[i])
		}
		if op != "isnull" && op != "notnull" && val == "" {
			continue
		}
		out = append(out, Filter{Column: name, Op: op, Value: val})
		if len(out) >= 12 {
			break
		}
	}
	return out
}

func quoteLiteral(s string) string {
	s = strings.ReplaceAll(s, "\x00", "")
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func escapeLike(s string) string {
	return strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_").Replace(s)
}

// whereClause 生成 WHERE 子句(不含 WHERE 关键字),值以字面量安全转义。
func whereClause(filters []Filter) string {
	if len(filters) == 0 {
		return ""
	}
	parts := make([]string, 0, len(filters))
	for _, f := range filters {
		col := quoteIdent(f.Column)
		switch f.Op {
		case "isnull":
			parts = append(parts, col+" IS NULL")
		case "notnull":
			parts = append(parts, col+" IS NOT NULL")
		case "contains":
			parts = append(parts, col+" ILIKE "+quoteLiteral("%"+escapeLike(f.Value)+"%")+" ESCAPE '\\'")
		case "starts":
			parts = append(parts, col+" ILIKE "+quoteLiteral(escapeLike(f.Value)+"%")+" ESCAPE '\\'")
		case "like":
			parts = append(parts, col+" LIKE "+quoteLiteral(f.Value))
		default:
			parts = append(parts, col+" "+f.Op+" "+quoteLiteral(f.Value))
		}
	}
	return strings.Join(parts, " AND ")
}

// filterQuery 把筛选与隐藏列重新编码为查询串(不含前缀 &)。
func opLabel(op string) string {
	for _, o := range filterOptions {
		if o.Value == op {
			return o.Label
		}
	}
	return op
}

func filterQuery(filters []Filter, hidden []string) string {
	v := url.Values{}
	for _, f := range filters {
		v.Add("f.col", f.Column)
		v.Add("f.op", f.Op)
		v.Add("f.val", f.Value)
	}
	if len(hidden) > 0 {
		v.Set("hide", strings.Join(hidden, ","))
	}
	return v.Encode()
}

func parseHidden(q url.Values, cols []Column) []string {
	seen := map[string]bool{}
	var out []string
	for _, raw := range q["hide"] {
		for _, name := range strings.Split(raw, ",") {
			name = strings.TrimSpace(name)
			if name == "" || seen[name] || !hasColumn(cols, name) {
				continue
			}
			seen[name] = true
			out = append(out, name)
		}
	}
	return out
}

func visibleColumns(cols []Column, hidden []string) []Column {
	if len(hidden) == 0 {
		return cols
	}
	skip := map[string]bool{}
	for _, h := range hidden {
		skip[h] = true
	}
	out := make([]Column, 0, len(cols))
	for _, c := range cols {
		if !skip[c.Name] {
			out = append(out, c)
		}
	}
	return out
}

func columnIndexes(cols []Column) map[string]int {
	m := make(map[string]int, len(cols))
	for i, c := range cols {
		m[c.Name] = i
	}
	return m
}

func primaryKey(cols []Column) []Column {
	var pk []Column
	for _, c := range cols {
		if c.PK {
			pk = append(pk, c)
		}
	}
	return pk
}

// encodeRowKey 用主键值生成 URL 安全的行标识。
func encodeRowKey(values map[string]string) string {
	if len(values) == 0 {
		return ""
	}
	data, err := json.Marshal(values)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(data)
}

func decodeRowKey(s string) (map[string]string, error) {
	if s == "" {
		return nil, fmt.Errorf("缺少行标识")
	}
	data, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("行标识无效")
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("行标识无效")
	}
	return m, nil
}
