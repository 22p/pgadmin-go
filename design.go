package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

type DesignColumn struct {
	Name    string
	Type    string
	Default string
	NotNull bool
	PK      bool
}

type DesignView struct {
	Schema  string
	Table   string
	Mode    string
	Columns []DesignColumn
	Schemas []string
	Blank   []int
}

var typePattern = regexp.MustCompile("^[A-Za-z][A-Za-z0-9_ ]{0,40}(\\([0-9]+(,[0-9]+)?\\))?(\\[\\])?$")

func validType(t string) bool {
	return typePattern.MatchString(strings.TrimSpace(t))
}

func validIdent(name string) bool {
	if name == "" || len(name) > 63 || strings.ContainsRune(name, 0) {
		return false
	}
	for _, r := range name {
		if r < 0x20 {
			return false
		}
	}
	return true
}

func (s *server) handleDesign(w http.ResponseWriter, r *http.Request, sess *Session) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	db := r.PathValue("db")
	p := s.newPage(sess, "建表 / 改表")
	p.CurDB = db
	p.Flash = r.URL.Query().Get("msg")
	p.CurSchema = r.URL.Query().Get("schema")
	p.CurTable = r.URL.Query().Get("table")
	s.fillNav(ctx, sess, p, db)

	view := &DesignView{Schema: p.CurSchema, Table: p.CurTable, Mode: "create", Blank: []int{0, 1, 2, 3}}
	if view.Schema == "" {
		view.Schema = "public"
	}
	for _, schema := range p.Schemas {
		view.Schemas = append(view.Schemas, schema.Name)
	}
	if view.Table != "" {
		view.Mode = "alter"
		pool, err := sess.Pool(db)
		if err != nil {
			p.Err = err.Error()
			p.Design = view
			s.render(w, "design", p)
			return
		}
		cols, err := describeTable(ctx, pool, view.Schema, view.Table)
		if err != nil {
			p.Err = err.Error()
			p.Design = view
			s.render(w, "design", p)
			return
		}
		for _, c := range cols {
			view.Columns = append(view.Columns, DesignColumn{
				Name: c.Name, Type: c.Type, Default: c.Default, NotNull: !c.Nullable, PK: c.PK,
			})
		}
	}
	p.Design = view
	s.render(w, "design", p)
}

func collectColumns(r *http.Request) ([]DesignColumn, error) {
	names := r.Form["c.name"]
	types := r.Form["c.type"]
	defs := r.Form["c.def"]
	notnulls := r.Form["c.notnull"]
	pks := r.Form["c.pk"]
	if len(names) != len(types) || len(names) != len(defs) || len(names) != len(notnulls) || len(names) != len(pks) {
		return nil, fmt.Errorf("列定义不完整,请刷新后重试")
	}
	var out []DesignColumn
	seen := map[string]bool{}
	for i, name := range names {
		name = strings.TrimSpace(name)
		typ := strings.TrimSpace(types[i])
		if name == "" && typ == "" {
			continue
		}
		if !validIdent(name) {
			return nil, fmt.Errorf("列名无效: %q", name)
		}
		if seen[name] {
			return nil, fmt.Errorf("列名重复: %s", name)
		}
		seen[name] = true
		if !validType(typ) {
			return nil, fmt.Errorf("类型无效: %q(示例:numeric(10,2)、text、timestamptz)", typ)
		}
		def := strings.TrimSpace(defs[i])
		if strings.Contains(def, ";") || strings.Contains(def, "--") || strings.Contains(def, "/*") {
			return nil, fmt.Errorf("默认值包含非法字符: %q", def)
		}
		out = append(out, DesignColumn{
			Name:    name,
			Type:    typ,
			Default: def,
			NotNull: notnulls[i] == "1",
			PK:      pks[i] == "1",
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("至少需要定义一列")
	}
	return out, nil
}

func columnDDL(c DesignColumn) string {
	parts := []string{quoteIdent(c.Name), c.Type}
	if c.NotNull {
		parts = append(parts, "NOT NULL")
	}
	if c.Default != "" {
		parts = append(parts, "DEFAULT "+c.Default)
	}
	return strings.Join(parts, " ")
}

func (s *server) handleDesignCreate(w http.ResponseWriter, r *http.Request, sess *Session) {
	if s.readonly {
		http.Error(w, "只读模式下不允许建表", http.StatusForbidden)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	db := r.PathValue("db")
	schema := strings.TrimSpace(r.FormValue("schema"))
	table := strings.TrimSpace(r.FormValue("table"))
	back := "/db/" + url.PathEscape(db) + "/design"
	msg := func(text string) {
		http.Redirect(w, r, back+"?msg="+url.QueryEscape(text), http.StatusSeeOther)
	}
	if schema == "" {
		schema = "public"
	}
	if !validIdent(table) {
		msg("表名无效")
		return
	}
	cols, err := collectColumns(r)
	if err != nil {
		msg(err.Error())
		return
	}

	var parts []string
	var pk []string
	for _, c := range cols {
		parts = append(parts, columnDDL(c))
		if c.PK {
			pk = append(pk, quoteIdent(c.Name))
		}
	}
	if len(pk) > 0 {
		parts = append(parts, "PRIMARY KEY ("+strings.Join(pk, ", ")+")")
	}
	stmt := "CREATE TABLE " + quoteIdent(schema, table) + " (\n  " + strings.Join(parts, ",\n  ") + "\n)"

	pool, err := sess.Pool(db)
	if err != nil {
		msg(err.Error())
		return
	}
	if _, err := pool.Exec(ctx, stmt); err != nil {
		msg("建表失败:" + err.Error())
		return
	}
	http.Redirect(w, r, "/db/"+url.PathEscape(db)+"/browse?schema="+url.QueryEscape(schema)+"&table="+url.QueryEscape(table)+"&msg="+url.QueryEscape("表已创建"), http.StatusSeeOther)
}

func (s *server) handleDesignAlter(w http.ResponseWriter, r *http.Request, sess *Session) {
	if s.readonly {
		http.Error(w, "只读模式下不允许修改表结构", http.StatusForbidden)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
	defer cancel()

	db := r.PathValue("db")
	schema := strings.TrimSpace(r.FormValue("schema"))
	table := strings.TrimSpace(r.FormValue("table"))
	back := "/db/" + url.PathEscape(db) + "/design?schema=" + url.QueryEscape(schema) + "&table=" + url.QueryEscape(table)
	msg := func(text string) {
		http.Redirect(w, r, back+"&msg="+url.QueryEscape(text), http.StatusSeeOther)
	}

	rel, err := relationName(schema, table)
	if err != nil {
		msg(err.Error())
		return
	}
	pool, err := sess.Pool(db)
	if err != nil {
		msg(err.Error())
		return
	}
	existing, err := describeTable(ctx, pool, schema, table)
	if err != nil {
		msg(err.Error())
		return
	}
	known := map[string]bool{}
	for _, c := range existing {
		known[c.Name] = true
	}

	var stmts []string
	cols, err := collectColumns(r)
	if err != nil && strings.Contains(err.Error(), "至少需要定义一列") {
		cols = nil
	} else if err != nil {
		msg(err.Error())
		return
	}
	for _, c := range cols {
		if known[c.Name] {
			msg("列已存在:" + c.Name)
			return
		}
		stmts = append(stmts, "ALTER TABLE "+rel+" ADD COLUMN "+columnDDL(c))
	}
	for _, name := range r.Form["d.col"] {
		if !known[name] {
			continue
		}
		stmts = append(stmts, "ALTER TABLE "+rel+" DROP COLUMN "+quoteIdent(name))
	}
	if len(stmts) == 0 {
		msg("没有需要执行的变更")
		return
	}
	for _, stmt := range stmts {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			msg("执行失败:" + err.Error())
			return
		}
	}
	http.Redirect(w, r, "/db/"+url.PathEscape(db)+"/structure?schema="+url.QueryEscape(schema)+"&table="+url.QueryEscape(table)+"&msg="+url.QueryEscape("表结构已更新"), http.StatusSeeOther)
}
