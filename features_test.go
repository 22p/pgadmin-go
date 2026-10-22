package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
)

func TestReadonlyTemplatesHideActions(t *testing.T) {
	page := &Page{
		Title: "只读", User: "u", Host: "h", Port: 5432, CSRF: "c", ReadOnly: true,
		DBs: []string{"demo"}, DBInfos: []DBInfo{{Name: "demo", Size: "1 MB"}},
		CurDB: "demo", CurSchema: "public", CurTable: "users", IsTable: true,
		Columns: []Column{{Name: "id", PK: true}}, AllColumns: []Column{{Name: "id", PK: true}},
		Sizes: []int{25}, Ops: &OpsView{}, Backup: &BackupView{},
	}
	check := func(name string, banned []string) {
		var buf bytes.Buffer
		if err := templates[name].ExecuteTemplate(&buf, "base", page); err != nil {
			t.Fatalf("渲染 %s 失败: %v", name, err)
		}
		out := buf.String()
		for _, b := range banned {
			if strings.Contains(out, b) {
				t.Fatalf("%s 在只读模式下不应包含 %q", name, b)
			}
		}
	}
	check("databases", []string{"创建数据库"})
	check("browse", []string{"新增行", "清空表", "删除表"})
	check("ops", []string{"终止"})
}

func TestReadonlyGuards(t *testing.T) {
	s := &server{sessions: map[string]*Session{}, readonly: true}
	sess := &Session{}
	reqs := []struct {
		name string
		h    func(http.ResponseWriter, *http.Request, *Session)
	}{
		{"row-save", s.handleRowSave},
		{"row-delete", s.handleRowDelete},
		{"table-action", s.handleTableAction},
		{"db-create", s.handleCreateDatabase},
		{"db-drop", s.handleDropDatabase},
		{"design-create", s.handleDesignCreate},
		{"design-alter", s.handleDesignAlter},
		{"backup-restore", s.handleBackupRestore},
		{"ops-action", s.handleOpsAction},
	}
	for _, tc := range reqs {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/db/demo/x", nil)
		tc.h(rec, req, sess)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s 在只读模式下应返回 403, got %d", tc.name, rec.Code)
		}
	}
}

func TestPrepareConfigReadonly(t *testing.T) {
	cfg, err := buildConnConfig(ConnSpec{Method: authPassword, Host: "127.0.0.1", User: "u", Database: "d"})
	if err != nil {
		t.Fatal(err)
	}
	s := &server{readonly: true}
	s.prepareConfig(cfg)
	if cfg.RuntimeParams["default_transaction_read_only"] != "on" {
		t.Fatalf("只读参数未注入: %+v", cfg.RuntimeParams)
	}
	s2 := &server{}
	s2.prepareConfig(cfg)
	if cfg.RuntimeParams["default_transaction_read_only"] != "on" {
		t.Fatal("非只读模式不应清空已有参数")
	}
}

func TestQuoteLiteral(t *testing.T) {
	if got := quoteLiteral("a'b"); got != "'a''b'" {
		t.Fatalf("quoteLiteral = %s", got)
	}
	if got := quoteLiteral("a\x00b"); got != "'ab'" {
		t.Fatalf("NUL 未剔除: %q", got)
	}
}

func TestWhereClause(t *testing.T) {
	got := whereClause([]Filter{
		{Column: "name", Op: "contains", Value: "a'b"},
		{Column: "id", Op: ">", Value: "5"},
		{Column: "bio", Op: "isnull"},
	})
	if !strings.Contains(got, "ILIKE '%a''b%'") {
		t.Fatalf("contains 条件异常: %s", got)
	}
	if !strings.Contains(got, " > '5'") {
		t.Fatalf("比较条件异常: %s", got)
	}
	if !strings.Contains(got, "IS NULL") {
		t.Fatalf("IS NULL 条件异常: %s", got)
	}
}

func TestParseFilters(t *testing.T) {
	cols := []Column{{Name: "name"}, {Name: "id"}}
	q := url.Values{}
	q.Add("f.col", "name")
	q.Add("f.op", "contains")
	q.Add("f.val", "abc")
	q.Add("f.col", "nope")
	q.Add("f.op", "=")
	q.Add("f.val", "1")
	q.Add("f.col", "id")
	q.Add("f.op", "isnull")
	q.Add("f.val", "")

	got := parseFilters(q, cols)
	if len(got) != 2 {
		t.Fatalf("条件数量 = %d: %+v", len(got), got)
	}
	if got[0].Column != "name" || got[0].Op != "contains" {
		t.Fatalf("第一个条件 = %+v", got[0])
	}
	if got[1].Column != "id" || got[1].Op != "isnull" {
		t.Fatalf("第二个条件 = %+v", got[1])
	}
}

func TestVisibleColumns(t *testing.T) {
	cols := []Column{{Name: "a"}, {Name: "b"}, {Name: "c"}}
	got := visibleColumns(cols, []string{"b", "missing"})
	if len(got) != 2 || got[0].Name != "a" || got[1].Name != "c" {
		t.Fatalf("visibleColumns = %+v", got)
	}
}

func TestRowKeyRoundTrip(t *testing.T) {
	key := encodeRowKey(map[string]string{"id": "42"})
	values, err := decodeRowKey(key)
	if err != nil || values["id"] != "42" {
		t.Fatalf("decode = %+v, %v", values, err)
	}
	if _, err := decodeRowKey("!!!"); err == nil {
		t.Fatal("非法 key 应报错")
	}
}

func TestValidType(t *testing.T) {
	ok := []string{"text", "numeric(10,2)", "timestamp with time zone", "int[]", "varchar(255)"}
	for _, typ := range ok {
		if !validType(typ) {
			t.Fatalf("应接受类型 %q", typ)
		}
	}
	bad := []string{"", "text;drop table x", "text--x", "text'", "1int"}
	for _, typ := range bad {
		if validType(typ) {
			t.Fatalf("应拒绝类型 %q", typ)
		}
	}
}

func TestColumnDDL(t *testing.T) {
	id := fmt.Sprintf("%q", "id")
	got := columnDDL(DesignColumn{Name: "id", Type: "serial", NotNull: true, PK: true})
	if got != id+" serial NOT NULL" {
		t.Fatalf("columnDDL = %q", got)
	}
	got = columnDDL(DesignColumn{Name: "created_at", Type: "timestamptz", Default: "now()"})
	if got != fmt.Sprintf("%q", "created_at")+" timestamptz DEFAULT now()" {
		t.Fatalf("columnDDL = %q", got)
	}
}

func TestCollectColumns(t *testing.T) {
	form := url.Values{}
	for _, row := range [][3]string{{"id", "serial", "1"}, {"name", "text", ""}, {"", "", ""}} {
		form.Add("c.name", row[0])
		form.Add("c.type", row[1])
		form.Add("c.def", "")
		form.Add("c.notnull", row[2])
		form.Add("c.pk", row[2])
	}
	req := httptest.NewRequest(http.MethodPost, "/design/create", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := req.ParseForm(); err != nil {
		t.Fatal(err)
	}
	cols, err := collectColumns(req)
	if err != nil {
		t.Fatalf("collectColumns: %v", err)
	}
	if len(cols) != 2 || cols[0].Name != "id" || !cols[0].NotNull || cols[1].Name != "name" {
		t.Fatalf("解析结果 = %+v", cols)
	}

	bad := url.Values{}
	for i := 0; i < 2; i++ {
		bad.Add("c.name", "a")
		bad.Add("c.type", "text")
		bad.Add("c.def", "")
		bad.Add("c.notnull", "")
		bad.Add("c.pk", "")
	}
	req = httptest.NewRequest(http.MethodPost, "/design/create", strings.NewReader(bad.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	_ = req.ParseForm()
	if _, err := collectColumns(req); err == nil {
		t.Fatal("重复列名应报错")
	}
}

// TestIntegrationBrowseFilter 验证浏览页筛选在真实数据库上的行为。
func TestIntegrationBrowseFilter(t *testing.T) {
	dsn := os.Getenv("PGTEST_DSN")
	if dsn == "" {
		t.Skip("未设置 PGTEST_DSN,跳过集成测试")
	}
	cfg, err := buildConnConfig(ConnSpec{Method: authDSN, DSN: dsn})
	if err != nil {
		t.Fatal(err)
	}
	setup := newSession(cfg, authDSN)
	defer setup.Close()
	pool, err := setup.Pool(cfg.Database)
	if err != nil {
		t.Fatalf("连接失败: %v", err)
	}
	ctx := context.Background()
	if _, err := pool.Exec(ctx, "DROP TABLE IF EXISTS pggo_browse_test"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "CREATE TABLE pggo_browse_test (id serial PRIMARY KEY, name text)"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "INSERT INTO pggo_browse_test (name) VALUES ('alpha'), ('beta'), ('gamma')"); err != nil {
		t.Fatal(err)
	}
	defer pool.Exec(context.Background(), "DROP TABLE IF EXISTS pggo_browse_test")

	s := &server{sessions: map[string]*Session{}, autoSpec: &ConnSpec{Method: authDSN, DSN: dsn}}
	defer s.closeAll()

	req := httptest.NewRequest(http.MethodGet, "/db/x/browse", nil)
	req.SetPathValue("db", cfg.Database)
	q := req.URL.Query()
	q.Set("schema", "public")
	q.Set("table", "pggo_browse_test")
	q.Add("f.col", "name")
	q.Add("f.op", "contains")
	q.Add("f.val", "alp")
	req.URL.RawQuery = q.Encode()

	rec := httptest.NewRecorder()
	s.withAuth(s.handleBrowse)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "已筛选") {
		t.Fatal("页面应显示筛选标记")
	}
	if !strings.Contains(body, "alpha") {
		t.Fatal("筛选结果应包含 alpha")
	}
	if strings.Contains(body, ">beta<") || strings.Contains(body, ">gamma<") {
		t.Fatal("筛选结果不应包含 beta / gamma")
	}
	if !strings.Contains(body, "f.col=name") {
		t.Fatal("分页链接应保留筛选条件")
	}
}
