package main

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// EditField 是行编辑表单中的一列。
type EditField struct {
	Name     string
	Type     string
	Value    string
	Nullable bool
	IsPK     bool
	Null     bool
}

// EditView 描述新增 / 编辑行的表单。
type EditView struct {
	Schema    string
	Table     string
	Mode      string // new | edit
	Key       string
	Fields    []EditField
	CanDelete bool
}

func (s *server) handleRowForm(w http.ResponseWriter, r *http.Request, sess *Session) {
	if s.readonly {
		http.Error(w, "只读模式下不允许编辑数据", http.StatusForbidden)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	db := r.PathValue("db")
	schema := r.URL.Query().Get("schema")
	table := r.URL.Query().Get("table")
	key := r.URL.Query().Get("key")
	mode := r.URL.Query().Get("mode")
	if mode != "new" {
		mode = "edit"
	}

	p := s.newPage(sess, "编辑数据")
	p.Tab = "browse"
	p.CurSchema, p.CurTable = schema, table
	s.fillNav(ctx, sess, p, db)

	pool, err := sess.Pool(db)
	if err != nil {
		p.Err = err.Error()
		s.render(w, "edit", p)
		return
	}
	cols, err := describeTable(ctx, pool, schema, table)
	if err != nil {
		p.Err = err.Error()
		s.render(w, "edit", p)
		return
	}

	view := &EditView{Schema: schema, Table: table, Mode: mode, Key: key}
	values := map[string]string{}
	nulls := map[string]bool{}
	rel, err := relationName(schema, table)
	if err != nil {
		p.Err = err.Error()
		s.render(w, "edit", p)
		return
	}

	if mode == "edit" {
		pk := primaryKey(cols)
		if len(pk) == 0 {
			p.Err = "该表没有主键,无法安全定位单行;请使用 SQL 编辑器修改"
			s.render(w, "edit", p)
			return
		}
		keyValues, err := decodeRowKey(key)
		if err != nil {
			p.Err = err.Error()
			s.render(w, "edit", p)
			return
		}
		var conds []string
		for _, c := range pk {
			v, ok := keyValues[c.Name]
			if !ok {
				p.Err = "行标识缺少主键列 " + c.Name
				s.render(w, "edit", p)
				return
			}
			conds = append(conds, quoteIdent(c.Name)+" = "+quoteLiteral(v))
		}
		_, rows, err := queryCells(ctx, pool, "SELECT * FROM "+rel+" WHERE "+strings.Join(conds, " AND ")+" LIMIT 1")
		if err != nil {
			p.Err = err.Error()
			s.render(w, "edit", p)
			return
		}
		if len(rows) == 0 {
			p.Err = "该行已不存在,可能已被其他会话删除"
			s.render(w, "edit", p)
			return
		}
		for i, c := range cols {
			if rows[0][i].Null {
				nulls[c.Name] = true
				continue
			}
			values[c.Name] = rows[0][i].Text
		}
		view.CanDelete = true
	}

	for _, c := range cols {
		view.Fields = append(view.Fields, EditField{
			Name:     c.Name,
			Type:     c.Type,
			Value:    values[c.Name],
			Nullable: c.Nullable,
			IsPK:     c.PK,
			Null:     nulls[c.Name],
		})
	}
	p.Edit = view
	s.render(w, "edit", p)
}

func (s *server) handleRowSave(w http.ResponseWriter, r *http.Request, sess *Session) {
	if s.readonly {
		http.Error(w, "只读模式下不允许写入数据", http.StatusForbidden)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	db := r.PathValue("db")
	schema := r.FormValue("schema")
	table := r.FormValue("table")
	mode := r.FormValue("mode")
	key := r.FormValue("key")

	pool, err := sess.Pool(db)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	cols, err := describeTable(ctx, pool, schema, table)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	rel, err := relationName(schema, table)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	back := "/db/" + url.PathEscape(db) + "/browse?schema=" + url.QueryEscape(schema) + "&table=" + url.QueryEscape(table)
	redirect := func(msg string) {
		http.Redirect(w, r, back+"&msg="+url.QueryEscape(msg), http.StatusSeeOther)
	}

	if mode == "new" {
		var names []string
		var args []any
		for _, c := range cols {
			v := r.FormValue("v." + c.Name)
			if v == "" {
				// 留空表示不写入该列,交由数据库默认值处理
				continue
			}
			names = append(names, quoteIdent(c.Name))
			args = append(args, v)
		}
		sqlText := "INSERT INTO " + rel + " DEFAULT VALUES"
		if len(names) > 0 {
			placeholders := make([]string, len(names))
			for i := range names {
				placeholders[i] = "$" + itoa(i+1)
			}
			sqlText = "INSERT INTO " + rel + " (" + strings.Join(names, ", ") + ") VALUES (" + strings.Join(placeholders, ", ") + ")"
		}
		if _, err := pool.Exec(ctx, sqlText, args...); err != nil {
			redirect("插入失败:" + err.Error())
			return
		}
		redirect("已新增一行")
		return
	}

	pk := primaryKey(cols)
	if len(pk) == 0 {
		redirect("该表没有主键,无法更新")
		return
	}
	keyValues, err := decodeRowKey(key)
	if err != nil {
		redirect(err.Error())
		return
	}
	pkSet := map[string]bool{}
	for _, c := range pk {
		pkSet[c.Name] = true
	}

	var sets []string
	var args []any
	for _, c := range cols {
		if pkSet[c.Name] {
			continue
		}
		v := r.FormValue("v." + c.Name)
		sets = append(sets, quoteIdent(c.Name)+" = $"+itoa(len(args)+1))
		if v == "" && c.Nullable {
			args = append(args, nil)
		} else {
			args = append(args, v)
		}
	}
	if len(sets) == 0 {
		redirect("没有可更新的列")
		return
	}
	var conds []string
	for _, c := range pk {
		v, ok := keyValues[c.Name]
		if !ok {
			redirect("行标识缺少主键列 " + c.Name)
			return
		}
		args = append(args, v)
		conds = append(conds, quoteIdent(c.Name)+" = $"+itoa(len(args)))
	}
	sqlText := "UPDATE " + rel + " SET " + strings.Join(sets, ", ") + " WHERE " + strings.Join(conds, " AND ")
	tag, err := pool.Exec(ctx, sqlText, args...)
	if err != nil {
		redirect("更新失败:" + err.Error())
		return
	}
	if tag.RowsAffected() == 0 {
		redirect("没有匹配的行,可能已被删除")
		return
	}
	redirect("已保存")
}

func (s *server) handleRowDelete(w http.ResponseWriter, r *http.Request, sess *Session) {
	if s.readonly {
		http.Error(w, "只读模式下不允许删除数据", http.StatusForbidden)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	db := r.PathValue("db")
	schema := r.FormValue("schema")
	table := r.FormValue("table")
	key := r.FormValue("key")

	pool, err := sess.Pool(db)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	cols, err := describeTable(ctx, pool, schema, table)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	pk := primaryKey(cols)
	back := "/db/" + url.PathEscape(db) + "/browse?schema=" + url.QueryEscape(schema) + "&table=" + url.QueryEscape(table)
	if len(pk) == 0 {
		http.Redirect(w, r, back+"&msg="+url.QueryEscape("该表没有主键,无法删除单行"), http.StatusSeeOther)
		return
	}
	keyValues, err := decodeRowKey(key)
	if err != nil {
		http.Redirect(w, r, back+"&msg="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	rel, err := relationName(schema, table)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var conds []string
	var args []any
	for _, c := range pk {
		v, ok := keyValues[c.Name]
		if !ok {
			http.Redirect(w, r, back+"&msg="+url.QueryEscape("行标识缺少主键列 "+c.Name), http.StatusSeeOther)
			return
		}
		args = append(args, v)
		conds = append(conds, quoteIdent(c.Name)+" = $"+itoa(len(args)))
	}
	tag, err := pool.Exec(ctx, "DELETE FROM "+rel+" WHERE "+strings.Join(conds, " AND "), args...)
	if err != nil {
		http.Redirect(w, r, back+"&msg="+url.QueryEscape("删除失败:"+err.Error()), http.StatusSeeOther)
		return
	}
	if tag.RowsAffected() == 0 {
		http.Redirect(w, r, back+"&msg="+url.QueryEscape("没有匹配的行"), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, back+"&msg="+url.QueryEscape("已删除一行"), http.StatusSeeOther)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
