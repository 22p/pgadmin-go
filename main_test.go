package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"log"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if err := loadTemplates(); err != nil {
		log.Fatalf("加载模板失败: %v", err)
	}
	os.Exit(m.Run())
}

func TestBuildConnConfigPassword(t *testing.T) {
	cfg, err := buildConnConfig(ConnSpec{
		Method:   authPassword,
		Host:     "db.internal",
		Port:     6543,
		User:     "app",
		Password: "p@ss word/with:chars",
		Database: "shop",
		SSLMode:  "disable",
	})
	if err != nil {
		t.Fatalf("buildConnConfig: %v", err)
	}
	if cfg.Host != "db.internal" || cfg.Port != 6543 {
		t.Fatalf("host/port = %s:%d", cfg.Host, cfg.Port)
	}
	if cfg.User != "app" || cfg.Password != "p@ss word/with:chars" {
		t.Fatalf("user/password = %q/%q", cfg.User, cfg.Password)
	}
	if cfg.Database != "shop" {
		t.Fatalf("database = %q", cfg.Database)
	}
	if cfg.TLSConfig != nil {
		t.Fatal("sslmode=disable 不应启用 TLS")
	}
}

func TestBuildConnConfigSocket(t *testing.T) {
	cfg, err := buildConnConfig(ConnSpec{
		Method:  authTrust,
		Socket:  true,
		Host:    "/tmp/pgsock",
		User:    "postgres",
		SSLMode: "disable",
	})
	if err != nil {
		t.Fatalf("buildConnConfig: %v", err)
	}
	if cfg.Host != "/tmp/pgsock" {
		t.Fatalf("socket host = %q", cfg.Host)
	}
	if cfg.Password != "" {
		t.Fatal("免密认证不应携带密码")
	}
	if cfg.Database != "postgres" {
		t.Fatalf("默认数据库 = %q", cfg.Database)
	}
}

func TestBuildConnConfigDSN(t *testing.T) {
	cfg, err := buildConnConfig(ConnSpec{
		Method: authDSN,
		DSN:    "postgres://u:p@db.example:6543/app?sslmode=require",
	})
	if err != nil {
		t.Fatalf("buildConnConfig: %v", err)
	}
	if cfg.Host != "db.example" || cfg.Port != 6543 || cfg.Database != "app" {
		t.Fatalf("dsn parsed = %s:%d/%s", cfg.Host, cfg.Port, cfg.Database)
	}
	if cfg.TLSConfig == nil {
		t.Fatal("sslmode=require 应启用 TLS")
	}

	switched := cfg.Copy()
	switched.Database = "other"
	if switched.Database != "other" || cfg.Database != "app" {
		t.Fatal("Copy 后修改数据库不应影响原配置")
	}

	if _, err := buildConnConfig(ConnSpec{Method: authDSN, DSN: "not a dsn"}); err == nil {
		t.Fatal("非法连接串应报错")
	}
}

func TestBuildConnConfigRequiresUser(t *testing.T) {
	if _, err := buildConnConfig(ConnSpec{Method: authPassword, Host: "127.0.0.1"}); err == nil {
		t.Fatal("缺少用户名应报错")
	}
}

func TestBuildConnConfigCertNeedsTLS(t *testing.T) {
	certPEM, keyPEM := selfSignedPEM(t)
	_, err := buildConnConfig(ConnSpec{
		Method:   authCert,
		Host:     "127.0.0.1",
		User:     "certuser",
		Database: "postgres",
		SSLMode:  "disable",
		CertPEM:  certPEM,
		KeyPEM:   keyPEM,
	})
	if err == nil || !strings.Contains(err.Error(), "TLS") {
		t.Fatalf("期望 TLS 相关错误, got %v", err)
	}
}

func TestBuildConnConfigCertLoadsKeyPair(t *testing.T) {
	certPEM, keyPEM := selfSignedPEM(t)
	cfg, err := buildConnConfig(ConnSpec{
		Method:   authCert,
		Host:     "db.internal",
		User:     "certuser",
		Database: "postgres",
		SSLMode:  "verify-full",
		CertPEM:  certPEM,
		KeyPEM:   keyPEM,
	})
	if err != nil {
		t.Fatalf("buildConnConfig: %v", err)
	}
	if cfg.TLSConfig == nil || len(cfg.TLSConfig.Certificates) != 1 {
		t.Fatal("客户端证书未挂载到 TLS 配置")
	}
	if cfg.Fallbacks != nil {
		t.Fatal("证书认证不应允许回退到明文")
	}
	if cfg.TLSConfig.ServerName != "db.internal" {
		t.Fatalf("ServerName = %q", cfg.TLSConfig.ServerName)
	}
}

func TestNewCellTruncates(t *testing.T) {
	short := newCell("hello", 300)
	if short.Text != "hello" || short.Title != "" {
		t.Fatalf("短文本被修改: %+v", short)
	}

	long := strings.Repeat("x", 400)
	cell := newCell(long, 300)
	if !strings.HasSuffix(cell.Text, "…") || len([]rune(cell.Text)) != 301 {
		t.Fatalf("截断异常: %d", len([]rune(cell.Text)))
	}
	if cell.Title != long {
		t.Fatal("完整内容应写入 title")
	}
}

func TestQuoteIdent(t *testing.T) {
	want := "\"we\"\"ird\".\"ta ble\""
	if got := quoteIdent("we\"ird", "ta ble"); got != want {
		t.Fatalf("quoteIdent = %s, want %s", got, want)
	}
}

func TestRelationNameRejectsEmpty(t *testing.T) {
	if _, err := relationName("public", ""); err == nil {
		t.Fatal("空表名应报错")
	}
}

func TestAuthLabel(t *testing.T) {
	cases := map[string]string{
		authPassword: "密码认证",
		authTrust:    "免密认证",
		authCert:     "客户端证书",
		authDSN:      "连接串",
		"unknown":    "密码认证",
	}
	for in, want := range cases {
		if got := authLabel(in); got != want {
			t.Fatalf("authLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestTemplatesRender 用样本数据渲染全部页面,防止模板语法或字段丢失。
func TestTemplatesRender(t *testing.T) {
	if err := loadTemplates(); err != nil {
		t.Fatalf("loadTemplates: %v", err)
	}
	page := &Page{
		Title:       "测试",
		User:        "pgadmin",
		Host:        "127.0.0.1",
		Port:        5432,
		CSRF:        "csrf",
		MethodLabel: "密码认证",
		DBs:         []string{"demo"},
		DBInfos:     []DBInfo{{Name: "demo", Size: "8 MB"}},
		CurDB:       "demo",
		Schemas: []SchemaNode{{
			Name:   "public",
			Tables: []TableRef{{Schema: "public", Name: "users", Kind: "TABLE"}},
		}},
		CurSchema: "public",
		CurTable:  "users",
		IsTable:   true,
		Tab:       "browse",
		Columns: []Column{
			{Name: "id", Type: "integer", PK: true},
			{Name: "name", Type: "text", Nullable: true, Default: "'x'"},
		},
		Rows:       [][]Cell{{{Text: "1"}, {Null: true}}},
		Page:       1,
		PagePrev:   0,
		PageNext:   2,
		Size:       50,
		Total:      1,
		Offset:     0,
		FirstRow:   1,
		LastRow:    1,
		Sort:       "id",
		Dir:        "asc",
		Sizes:      []int{25, 50, 100, 200},
		Indexes:    []Index{{Name: "users_pkey", Def: "CREATE UNIQUE INDEX ..."}},
		FKs:        []FK{{Name: "users_fk", Def: "FOREIGN KEY ..."}},
		TableSize:  "16 kB",
		EstRows:    1,
		SQLText:    "select 1",
		Sets:       []ResultSet{{Columns: []string{"n"}, Rows: [][]Cell{{{Text: "1"}}}}},
		Tag:        "SELECT 1",
		Duration:   "1ms",
		AuthMethod: authCert,
		FormHost:   "127.0.0.1",
		FormPort:   5432,
		FormUser:   "postgres",
		FormDB:     "postgres",
		FormSSL:    "require",
		FormDSN:    "postgres://u@h/db",

		AllColumns:    []Column{{Name: "id", Type: "integer", PK: true}, {Name: "name", Type: "text"}, {Name: "bio", Type: "text"}},
		HiddenCols:    map[string]bool{"bio": true},
		HiddenList:    []string{"bio"},
		Filters:       []Filter{{Column: "name", Op: "contains", Value: "x"}},
		FilterChips:   []FilterChip{{Label: "name 包含 x", RemoveURL: "/db/demo/browse?schema=public&table=users"}},
		FilterOptions: filterOptions,
		CanEdit:       true,
		PKCols:        []Column{{Name: "id", Type: "integer", PK: true}},
		RowKeys:       []string{"abc"},
		Edit: &EditView{
			Schema: "public", Table: "users", Mode: "edit", Key: "abc", CanDelete: true,
			Fields: []EditField{
				{Name: "id", Type: "integer", Value: "1", IsPK: true},
				{Name: "name", Type: "text", Value: "a", Nullable: true, Null: true},
			},
		},
		Ops: &OpsView{
			Version: "18.6", Size: "8 MB", Active: 1, Idle: 2, MaxConns: 100, HitRatio: "99.5",
			Activities: []Activity{{PID: 12, User: "pgadmin", DB: "demo", State: "active", Wait: "Client:ClientRead", Started: "10:00:00", Elapsed: "0.5", Query: "select 1", Client: "127.0.0.1"}},
			Locks:      []LockInfo{{PID: 12, User: "pgadmin", Relation: "public.users", Mode: "AccessShareLock", Granted: true}},
			TopTables:  []TableStat{{Name: "public.users", Size: "16 kB", Live: 137, SeqScan: 1, IdxScan: 5, LastVacuum: "10-01 12:00"}},
		},
		Backup: &BackupView{
			PgDump: "/usr/bin/pg_dump", PgRestore: "/usr/bin/pg_restore", Psql: "/usr/bin/psql",
			Schemas: []string{"public"}, Tables: []TableRef{{Schema: "public", Name: "users", Kind: "TABLE"}},
		},
		Design: &DesignView{
			Schema: "public", Mode: "create", Schemas: []string{"public"}, Blank: []int{0, 1},
			Columns: []DesignColumn{{Name: "id", Type: "integer", NotNull: true, PK: true}},
		},
		ER: &ERView{
			Schema: "public", Schemas: []string{"public"}, Width: 600, Height: 300, TableCount: 2,
			Nodes: []ERNode{{Name: "users", X: 10, Y: 10, W: 240, H: 90,
				Columns: []ERColumn{{Name: "id", Type: "integer", PK: true, Y: 54}}}},
			Edges: []EREdge{{Path: "M 0 0 C 10 0, 20 0, 30 0"}},
		},
		History:      []SQLHistory{{SQL: "select 1", Tag: "SELECT 1", Duration: time.Millisecond, At: time.Now()}},
		Favorites:    []SQLFavorite{{Name: "常量", SQL: "select 1"}},
		LastResultID: "abcdef0123456789",
	}

	for _, name := range []string{"login", "databases", "browse", "structure", "sql", "edit", "ops", "backup", "design", "er"} {
		var buf bytes.Buffer
		if err := templates[name].ExecuteTemplate(&buf, "base", page); err != nil {
			t.Fatalf("渲染 %s 失败: %v", name, err)
		}
		if buf.Len() < 200 {
			t.Fatalf("渲染 %s 输出过短: %d", name, buf.Len())
		}
	}
}

// TestIntegrationSession 需要真实数据库: PGTEST_DSN=postgres://user:pass@host:5432/db
func TestIntegrationSession(t *testing.T) {
	dsn := os.Getenv("PGTEST_DSN")
	if dsn == "" {
		t.Skip("未设置 PGTEST_DSN,跳过集成测试")
	}
	cfg, err := buildConnConfig(ConnSpec{Method: authDSN, DSN: dsn})
	if err != nil {
		t.Fatalf("buildConnConfig: %v", err)
	}
	sess := newSession(cfg, authDSN)
	defer sess.Close()

	pool, err := sess.Pool(cfg.Database)
	if err != nil {
		t.Fatalf("连接失败: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	sets, tag, err := runSQL(ctx, pool, "select 1 as one, null::text as nothing; select 2 as two")
	if err != nil {
		t.Fatalf("runSQL: %v", err)
	}
	if len(sets) != 2 {
		t.Fatalf("结果集数量 = %d", len(sets))
	}
	if sets[0].Rows[0][0].Text != "1" || !sets[0].Rows[0][1].Null {
		t.Fatalf("第一行结果异常: %+v", sets[0].Rows[0])
	}
	if !strings.HasPrefix(tag, "SELECT") {
		t.Fatalf("命令标签 = %q", tag)
	}

	if _, err := listDatabases(ctx, pool); err != nil {
		t.Fatalf("listDatabases: %v", err)
	}
	if _, err := loadTree(ctx, pool); err != nil {
		t.Fatalf("loadTree: %v", err)
	}
}

func selfSignedPEM(t *testing.T) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("生成私钥: %v", err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "pggo-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("签发证书: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("编码私钥: %v", err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM
}

// TestIntegrationAutoLogin 验证环境变量自动登录可直达管理台。
func TestIntegrationAutoLogin(t *testing.T) {
	dsn := os.Getenv("PGTEST_DSN")
	if dsn == "" {
		t.Skip("未设置 PGTEST_DSN,跳过集成测试")
	}
	s := &server{
		sessions: map[string]*Session{},
		autoSpec: &ConnSpec{Method: authDSN, DSN: dsn},
	}
	defer s.closeAll()

	rec := httptest.NewRecorder()
	s.withAuth(s.handleDatabases)(rec, httptest.NewRequest(http.MethodGet, "/databases", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "数据库") {
		t.Fatal("页面应包含数据库列表")
	}
	if s.autoSess == nil {
		t.Fatal("自动登录会话未创建")
	}
	if _, err := s.autoSession(); err != nil {
		t.Fatalf("复用自动登录会话失败: %v", err)
	}
}

func TestAutoLoginSpec(t *testing.T) {
	keys := []string{
		"PGGO_DSN", "PGGO_USER", "PGGO_PASSWORD", "PGGO_HOST", "PGGO_PORT",
		"PGGO_DB", "PGGO_AUTH", "PGGO_SSLMODE", "PGGO_SOCKET",
		"PGGO_SSLROOT", "PGGO_SSLCERT", "PGGO_SSLKEY",
	}
	for _, k := range keys {
		t.Setenv(k, "")
	}

	if spec, err := autoLoginSpec(); err != nil || spec != nil {
		t.Fatalf("未配置环境变量时应返回 nil, got %+v, %v", spec, err)
	}

	t.Setenv("PGGO_USER", "app")
	t.Setenv("PGGO_PASSWORD", "secret")
	t.Setenv("PGGO_HOST", "db.local")
	t.Setenv("PGGO_PORT", "6543")
	t.Setenv("PGGO_DB", "shop")
	t.Setenv("PGGO_SSLMODE", "require")
	t.Setenv("PGGO_SOCKET", "true")
	spec, err := autoLoginSpec()
	if err != nil {
		t.Fatalf("autoLoginSpec: %v", err)
	}
	if spec.Method != authPassword || spec.Host != "db.local" || spec.Port != 6543 ||
		spec.User != "app" || spec.Password != "secret" || spec.Database != "shop" ||
		spec.SSLMode != "require" || !spec.Socket {
		t.Fatalf("解析结果异常: %+v", spec)
	}

	t.Setenv("PGGO_DSN", "postgres://u:p@h:5432/db")
	if spec, err = autoLoginSpec(); err != nil || spec.Method != authDSN {
		t.Fatalf("PGGO_DSN 应优先: %+v, %v", spec, err)
	}

	t.Setenv("PGGO_DSN", "")
	t.Setenv("PGGO_AUTH", "oauth")
	if _, err := autoLoginSpec(); err == nil {
		t.Fatal("不支持的 PGGO_AUTH 应报错")
	}
	t.Setenv("PGGO_AUTH", "password")
	t.Setenv("PGGO_PORT", "abc")
	if _, err := autoLoginSpec(); err == nil {
		t.Fatal("非法 PGGO_PORT 应报错")
	}
}

func TestAutoLoginRedirectsLoginPage(t *testing.T) {
	s := &server{
		sessions: map[string]*Session{},
		autoSpec: &ConnSpec{Method: authDSN, DSN: "postgres://u@h/db"},
	}
	rec := httptest.NewRecorder()
	s.handleLoginForm(rec, httptest.NewRequest(http.MethodGet, "/login", nil))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("状态码 = %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/databases" {
		t.Fatalf("跳转地址 = %q", loc)
	}
}

func TestAutoLoginHidesLogoutButton(t *testing.T) {
	if err := loadTemplates(); err != nil {
		t.Fatalf("loadTemplates: %v", err)
	}
	page := &Page{
		Title:     "数据",
		User:      "app",
		Host:      "db.local",
		Port:      5432,
		CSRF:      "csrf",
		AutoLogin: true,
		CurDB:     "shop",
		Tab:       "sql",
		Sizes:     []int{25, 50},
	}
	var buf bytes.Buffer
	if err := templates["sql"].ExecuteTemplate(&buf, "base", page); err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	html := buf.String()
	if strings.Contains(html, "action=\"/logout\"") {
		t.Fatal("自动登录模式下不应渲染退出按钮")
	}
	if !strings.Contains(html, "自动登录") {
		t.Fatal("自动登录模式下应显示提示标签")
	}
}
