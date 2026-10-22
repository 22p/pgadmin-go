package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type DBInfo struct {
	Name string
	Size string
}

type Page struct {
	Title       string
	User        string
	Host        string
	Port        int
	CSRF        string
	Flash       string
	Err         string
	MethodLabel string
	AutoLogin   bool

	DBs     []string
	DBInfos []DBInfo
	CurDB   string
	Schemas []SchemaNode

	CurSchema string
	CurTable  string
	IsTable   bool
	Tab       string

	// 登录表单
	AuthMethod string
	FormHost   string
	FormPort   int
	FormUser   string
	FormDB     string
	FormSSL    string
	FormSocket bool
	FormDSN    string

	ReadOnly bool

	// 数据浏览
	Columns            []Column
	AllColumns         []Column
	HiddenCols         map[string]bool
	HiddenList         []string
	Filters            []Filter
	FilterChips        []FilterChip
	FilterOptions      []FilterOption
	FilterQS           string
	CanEdit            bool
	PKCols             []Column
	RowKeys            []string
	Rows               [][]Cell
	Page               int
	PagePrev, PageNext int
	Size               int
	Total              int64
	Offset             int64
	FirstRow, LastRow  int64
	HasPrev, HasNext   bool
	Sort, Dir          string
	Sizes              []int

	// 表结构
	Indexes   []Index
	FKs       []FK
	TableSize string
	EstRows   int64

	Edit   *EditView
	Ops    *OpsView
	Backup *BackupView
	Design *DesignView
	ER     *ERView

	// SQL
	SQLText      string
	Sets         []ResultSet
	Tag          string
	Duration     string
	History      []SQLHistory
	Favorites    []SQLFavorite
	LastResultID string
}

func (s *server) newPage(sess *Session, title string) *Page {
	return &Page{
		Title:       title,
		User:        sess.User,
		Host:        sess.Host,
		Port:        int(sess.Port),
		CSRF:        sess.CSRF,
		MethodLabel: authLabel(sess.Method),
		AutoLogin:   s.autoEnabled(),
		ReadOnly:    s.readonly,
		Sizes:       []int{25, 50, 100, 200},
	}
}

func (s *server) fillNav(ctx context.Context, sess *Session, p *Page, db string) {
	p.CurDB = db
	pool, err := sess.Pool(db)
	if err != nil {
		p.Err = err.Error()
		return
	}
	if dbs, err := listDatabases(ctx, pool); err == nil {
		p.DBs = dbs
	}
	tree, err := loadTree(ctx, pool)
	if err != nil {
		p.Err = err.Error()
		return
	}
	p.Schemas = tree
	for _, node := range tree {
		if node.Name != p.CurSchema {
			continue
		}
		for _, t := range node.Tables {
			if t.Name == p.CurTable && t.Kind == "TABLE" {
				p.IsTable = true
			}
		}
	}
}

func intParam(r *http.Request, key string, def int) int {
	v := r.URL.Query().Get(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func (s *server) handleDatabases(w http.ResponseWriter, r *http.Request, sess *Session) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	p := s.newPage(sess, "数据库")
	p.Flash = r.URL.Query().Get("msg")
	p.CurDB = sess.DB()
	if p.CurDB == "" {
		p.CurDB = "postgres"
	}

	pool, err := sess.Pool(p.CurDB)
	if err != nil {
		p.Err = err.Error()
		s.render(w, "databases", p)
		return
	}
	if p.DBs, err = listDatabases(ctx, pool); err != nil {
		p.Err = err.Error()
	}
	if infos, err := listDBInfos(ctx, pool); err != nil {
		p.Err = err.Error()
	} else {
		p.DBInfos = infos
	}
	s.render(w, "databases", p)
}

func (s *server) handleCreateDatabase(w http.ResponseWriter, r *http.Request, sess *Session) {
	if s.readonly {
		http.Error(w, "只读模式下不允许创建数据库", http.StatusForbidden)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	name := strings.TrimSpace(r.FormValue("name"))
	back := "/databases"
	if name == "" {
		http.Redirect(w, r, back+"?msg="+url.QueryEscape("数据库名不能为空"), http.StatusSeeOther)
		return
	}
	pool, err := sess.Pool(sess.DB())
	if err != nil {
		http.Redirect(w, r, back+"?msg="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	if _, err := pool.Exec(ctx, "CREATE DATABASE "+quoteIdent(name)); err != nil {
		http.Redirect(w, r, back+"?msg="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, back+"?msg="+url.QueryEscape("数据库 "+name+" 已创建"), http.StatusSeeOther)
}

func (s *server) handleDropDatabase(w http.ResponseWriter, r *http.Request, sess *Session) {
	if s.readonly {
		http.Error(w, "只读模式下不允许删除数据库", http.StatusForbidden)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	name := strings.TrimSpace(r.FormValue("name"))
	back := "/databases"
	if name == "" {
		http.Redirect(w, r, back, http.StatusSeeOther)
		return
	}
	if name == sess.DB() {
		http.Redirect(w, r, back+"?msg="+url.QueryEscape("不能删除当前正在使用的数据库"), http.StatusSeeOther)
		return
	}
	pool, err := sess.Pool(sess.DB())
	if err != nil {
		http.Redirect(w, r, back+"?msg="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	if _, err := pool.Exec(ctx, "DROP DATABASE "+quoteIdent(name)); err != nil {
		http.Redirect(w, r, back+"?msg="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, back+"?msg="+url.QueryEscape("数据库 "+name+" 已删除"), http.StatusSeeOther)
}

func (s *server) handleBrowse(w http.ResponseWriter, r *http.Request, sess *Session) {
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	db := r.PathValue("db")
	p := s.newPage(sess, "数据")
	p.Tab = "browse"
	p.Flash = r.URL.Query().Get("msg")
	p.CurSchema = r.URL.Query().Get("schema")
	p.CurTable = r.URL.Query().Get("table")
	s.fillNav(ctx, sess, p, db)

	if p.CurTable == "" {
		s.render(w, "browse", p)
		return
	}

	pool, err := sess.Pool(db)
	if err != nil {
		p.Err = err.Error()
		s.render(w, "browse", p)
		return
	}

	cols, err := describeTable(ctx, pool, p.CurSchema, p.CurTable)
	if err != nil {
		p.Err = err.Error()
		s.render(w, "browse", p)
		return
	}
	size := intParam(r, "size", 50)
	switch size {
	case 25, 50, 100, 200:
	default:
		size = 50
	}
	p.Size = size

	p.AllColumns = cols
	p.Filters = parseFilters(r.URL.Query(), cols)
	p.FilterOptions = filterOptions
	hidden := parseHidden(r.URL.Query(), cols)
	p.HiddenCols = make(map[string]bool, len(hidden))
	for _, h := range hidden {
		p.HiddenCols[h] = true
	}
	p.HiddenList = hidden
	p.Columns = visibleColumns(cols, hidden)
	if len(p.Columns) == 0 {
		p.Err = "所有列都被隐藏,请取消隐藏后重试"
		s.render(w, "browse", p)
		return
	}
	p.FilterQS = filterQuery(p.Filters, hidden)
	for i, f := range p.Filters {
		rest := make([]Filter, 0, len(p.Filters)-1)
		rest = append(rest, p.Filters[:i]...)
		rest = append(rest, p.Filters[i+1:]...)
		qs := filterQuery(rest, hidden)
		link := "/db/" + url.PathEscape(db) + "/browse?" + url.Values{
			"schema": {p.CurSchema},
			"table":  {p.CurTable},
			"size":   {strconv.Itoa(size)},
		}.Encode()
		if qs != "" {
			link += "&" + qs
		}
		label := f.Column + " " + opLabel(f.Op)
		if f.Op != "isnull" && f.Op != "notnull" {
			label += " " + f.Value
		}
		p.FilterChips = append(p.FilterChips, FilterChip{Label: label, RemoveURL: link})
	}
	where := whereClause(p.Filters)

	p.PKCols = primaryKey(cols)
	p.CanEdit = !s.readonly && len(p.PKCols) > 0

	pageNum := intParam(r, "page", 1)
	if pageNum < 1 {
		pageNum = 1
	}

	sortCol := r.URL.Query().Get("sort")
	valid := false
	for _, c := range cols {
		if c.Name == sortCol {
			valid = true
			break
		}
	}
	if !valid {
		if len(cols) == 0 {
			p.Err = "该对象没有可显示的列"
			s.render(w, "browse", p)
			return
		}
		sortCol = cols[0].Name
	}
	dir := strings.ToLower(r.URL.Query().Get("dir"))
	if dir != "desc" {
		dir = "asc"
	}
	p.Sort, p.Dir = sortCol, dir

	total, err := countRows(ctx, pool, p.CurSchema, p.CurTable, where)
	if err != nil {
		p.Err = err.Error()
		s.render(w, "browse", p)
		return
	}
	p.Total = total

	offset := int64(pageNum-1) * int64(size)
	if offset >= total && total > 0 {
		pageNum = 1
		offset = 0
	}
	p.Page = pageNum
	p.Offset = offset

	p.Rows, err = browseRows(ctx, pool, p.CurSchema, p.CurTable, where, sortCol, dir, size, offset)
	if err != nil {
		p.Err = err.Error()
		s.render(w, "browse", p)
		return
	}

	if p.CanEdit {
		idx := columnIndexes(cols)
		p.RowKeys = make([]string, 0, len(p.Rows))
		for _, row := range p.Rows {
			key := make(map[string]string, len(p.PKCols))
			for _, pk := range p.PKCols {
				key[pk.Name] = row[idx[pk.Name]].Text
			}
			p.RowKeys = append(p.RowKeys, encodeRowKey(key))
		}
	}
	if len(hidden) > 0 {
		idx := columnIndexes(cols)
		shown := make([]int, 0, len(p.Columns))
		for _, c := range p.Columns {
			shown = append(shown, idx[c.Name])
		}
		for i := range p.Rows {
			row := make([]Cell, 0, len(shown))
			for _, j := range shown {
				row = append(row, p.Rows[i][j])
			}
			p.Rows[i] = row
		}
	}

	p.FirstRow = offset + 1
	p.LastRow = offset + int64(len(p.Rows))
	if total == 0 {
		p.FirstRow = 0
	}
	p.HasPrev = pageNum > 1
	p.HasNext = offset+int64(len(p.Rows)) < total
	p.PagePrev = pageNum - 1
	p.PageNext = pageNum + 1

	s.render(w, "browse", p)
}

func (s *server) handleStructure(w http.ResponseWriter, r *http.Request, sess *Session) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	db := r.PathValue("db")
	p := s.newPage(sess, "结构")
	p.Tab = "structure"
	p.Flash = r.URL.Query().Get("msg")
	p.CurSchema = r.URL.Query().Get("schema")
	p.CurTable = r.URL.Query().Get("table")
	s.fillNav(ctx, sess, p, db)

	if p.CurTable == "" {
		s.render(w, "structure", p)
		return
	}

	pool, err := sess.Pool(db)
	if err != nil {
		p.Err = err.Error()
		s.render(w, "structure", p)
		return
	}

	if p.Columns, err = describeTable(ctx, pool, p.CurSchema, p.CurTable); err != nil {
		p.Err = err.Error()
		s.render(w, "structure", p)
		return
	}
	if p.Indexes, err = tableIndexes(ctx, pool, p.CurSchema, p.CurTable); err != nil {
		p.Err = err.Error()
	}
	if p.FKs, err = tableFKs(ctx, pool, p.CurSchema, p.CurTable); err != nil {
		p.Err = err.Error()
	}
	if size, est, err := tableStats(ctx, pool, p.CurSchema, p.CurTable); err != nil {
		p.Err = err.Error()
	} else {
		p.TableSize = size
		p.EstRows = est
	}
	s.render(w, "structure", p)
}

func (s *server) handleSQLForm(w http.ResponseWriter, r *http.Request, sess *Session) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	db := r.PathValue("db")
	p := s.newPage(sess, "SQL")
	p.Tab = "sql"
	p.Flash = r.URL.Query().Get("msg")
	p.CurSchema = r.URL.Query().Get("schema")
	p.CurTable = r.URL.Query().Get("table")
	p.SQLText = r.URL.Query().Get("sql")
	p.History = sess.History()
	p.Favorites = sess.Favorites()
	p.LastResultID = sess.LastResultID()
	s.fillNav(ctx, sess, p, db)
	s.render(w, "sql", p)
}

func (s *server) handleSQLExec(w http.ResponseWriter, r *http.Request, sess *Session) {
	ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
	defer cancel()

	db := r.PathValue("db")
	sqlText := r.FormValue("sql")

	p := s.newPage(sess, "SQL")
	p.Tab = "sql"
	p.SQLText = sqlText
	s.fillNav(ctx, sess, p, db)

	if strings.TrimSpace(sqlText) == "" {
		p.Err = "请输入 SQL 语句"
		s.render(w, "sql", p)
		return
	}

	pool, err := sess.Pool(db)
	if err != nil {
		p.Err = err.Error()
		s.render(w, "sql", p)
		return
	}

	start := time.Now()
	sets, tag, err := runSQL(ctx, pool, sqlText)
	elapsed := time.Since(start)
	p.Duration = elapsed.Round(time.Millisecond).String()
	p.Sets, p.Tag = sets, tag

	entry := SQLHistory{SQL: sqlText, Tag: tag, Duration: elapsed, At: time.Now()}
	if err != nil {
		p.Err = err.Error()
		entry.Err = err.Error()
	} else {
		p.LastResultID = sess.StoreResults(sets)
	}
	sess.AddHistory(entry)
	p.History = sess.History()
	p.Favorites = sess.Favorites()
	s.render(w, "sql", p)
}

func (s *server) handleSQLFavorite(w http.ResponseWriter, r *http.Request, sess *Session) {
	db := r.PathValue("db")
	back := "/db/" + url.PathEscape(db) + "/sql"
	sqlText := r.FormValue("sql")
	name := strings.TrimSpace(r.FormValue("name"))

	switch r.FormValue("action") {
	case "add":
		if name == "" {
			name = "未命名查询"
		}
		if strings.TrimSpace(sqlText) == "" {
			http.Redirect(w, r, back+"?msg="+url.QueryEscape("没有可收藏的 SQL"), http.StatusSeeOther)
			return
		}
		sess.AddFavorite(name, sqlText)
		http.Redirect(w, r, back+"?msg="+url.QueryEscape("已收藏:"+name)+"&sql="+url.QueryEscape(sqlText), http.StatusSeeOther)
	case "del":
		sess.RemoveFavorite(name)
		http.Redirect(w, r, back+"?msg="+url.QueryEscape("已取消收藏:"+name), http.StatusSeeOther)
	case "clear":
		sess.ClearHistory()
		http.Redirect(w, r, back+"?msg="+url.QueryEscape("历史已清空"), http.StatusSeeOther)
	default:
		http.Error(w, "未知操作", http.StatusBadRequest)
	}
}

func (s *server) handleSQLExport(w http.ResponseWriter, r *http.Request, sess *Session) {
	id := r.URL.Query().Get("id")
	sets, ok := sess.Results(id)
	if !ok {
		http.Error(w, "结果已过期,请重新执行查询", http.StatusNotFound)
		return
	}
	setIndex, err := strconv.Atoi(r.URL.Query().Get("set"))
	if err != nil || setIndex < 0 || setIndex >= len(sets) {
		http.Error(w, "结果集不存在", http.StatusBadRequest)
		return
	}
	set := sets[setIndex]
	format := strings.ToLower(r.URL.Query().Get("format"))
	if format == "" {
		format = "csv"
	}

	name := "query"
	if v := r.URL.Query().Get("name"); v != "" {
		name = strings.NewReplacer(" ", "_", "/", "_", "\\", "_", "\"", "").Replace(v)
	}

	switch format {
	case "csv":
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name+".csv"))
		_, _ = w.Write([]byte{0xEF, 0xBB, 0xBF})
		cw := csv.NewWriter(w)
		if err := cw.Write(set.Columns); err != nil {
			return
		}
		line := make([]string, len(set.Columns))
		for _, row := range set.Rows {
			for i := range line {
				if i < len(row) && !row[i].Null {
					line[i] = row[i].Text
				} else {
					line[i] = ""
				}
			}
			if err := cw.Write(line); err != nil {
				return
			}
		}
		cw.Flush()
	case "json":
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name+".json"))
		items := make([]map[string]any, 0, len(set.Rows))
		for _, row := range set.Rows {
			obj := make(map[string]any, len(set.Columns))
			for i, col := range set.Columns {
				if i < len(row) && !row[i].Null {
					obj[col] = row[i].Text
				} else {
					obj[col] = nil
				}
			}
			items = append(items, obj)
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(items)
	default:
		http.Error(w, "不支持的导出格式", http.StatusBadRequest)
	}
}

func (s *server) handleExportCSV(w http.ResponseWriter, r *http.Request, sess *Session) {
	db := r.PathValue("db")
	schema := r.URL.Query().Get("schema")
	table := r.URL.Query().Get("table")
	if table == "" {
		http.Error(w, "缺少 table 参数", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()

	pool, err := sess.Pool(db)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	cols, err := describeTable(ctx, pool, schema, table)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	hidden := parseHidden(r.URL.Query(), cols)
	cols = visibleColumns(cols, hidden)
	if len(cols) == 0 {
		http.Error(w, "没有可导出的列", http.StatusBadRequest)
		return
	}
	where := whereClause(parseFilters(r.URL.Query(), cols))
	sortCol := r.URL.Query().Get("sort")
	valid := false
	for _, c := range cols {
		if c.Name == sortCol {
			valid = true
		}
	}
	if !valid && len(cols) > 0 {
		sortCol = cols[0].Name
	}
	dir := strings.ToLower(r.URL.Query().Get("dir"))
	if dir != "desc" {
		dir = "asc"
	}
	rel, err := relationName(schema, table)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	filename := strings.NewReplacer(" ", "_", "/", "_", "\\", "_").Replace(schema+"."+table) + ".csv"
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	_, _ = w.Write([]byte{0xEF, 0xBB, 0xBF})

	cw := csv.NewWriter(w)
	defer cw.Flush()

	names := make([]string, len(cols))
	for i, c := range cols {
		names[i] = quoteIdent(c.Name)
	}
	sqlText := "SELECT " + strings.Join(names, ", ") + " FROM " + rel
	if where != "" {
		sqlText += " WHERE " + where
	}
	if sortCol != "" {
		sqlText += " ORDER BY " + quoteIdent(sortCol) + " " + dir
	}
	rows, err := pool.Query(ctx, sqlText, pgx.QueryExecModeSimpleProtocol)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	header := make([]string, len(cols))
	for i, c := range cols {
		header[i] = c.Name
	}
	if err := cw.Write(header); err != nil {
		return
	}

	line := make([]string, len(cols))
	for rows.Next() {
		raw := rows.RawValues()
		for i := range line {
			if i < len(raw) && raw[i] != nil {
				line[i] = string(raw[i])
			} else {
				line[i] = ""
			}
		}
		if err := cw.Write(line); err != nil {
			return
		}
	}
}

func (s *server) handleTableAction(w http.ResponseWriter, r *http.Request, sess *Session) {
	if s.readonly {
		http.Error(w, "只读模式下不允许修改表", http.StatusForbidden)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	db := r.PathValue("db")
	schema := r.FormValue("schema")
	table := r.FormValue("table")
	op := r.FormValue("op")

	back := "/db/" + url.PathEscape(db) + "/browse?schema=" + url.QueryEscape(schema) + "&table=" + url.QueryEscape(table)
	rel, err := relationName(schema, table)
	if err != nil {
		http.Redirect(w, r, back+"&msg="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}

	var stmt, msg string
	switch op {
	case "truncate":
		stmt, msg = "TRUNCATE TABLE "+rel, "已清空 "+schema+"."+table
	case "drop":
		stmt, msg = "DROP TABLE "+rel, "已删除 "+schema+"."+table
	default:
		http.Error(w, "未知操作", http.StatusBadRequest)
		return
	}

	pool, err := sess.Pool(db)
	if err != nil {
		http.Redirect(w, r, back+"&msg="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	if _, err := pool.Exec(ctx, stmt); err != nil {
		http.Redirect(w, r, back+"&msg="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	if op == "drop" {
		http.Redirect(w, r, "/db/"+url.PathEscape(db)+"/browse?msg="+url.QueryEscape(msg), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, back+"&msg="+url.QueryEscape(msg), http.StatusSeeOther)
}
