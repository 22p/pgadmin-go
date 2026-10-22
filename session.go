package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"github.com/jackc/pgx/v5"
	"io"
	"io/fs"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const cookieName = "pggo_sid"

const sessionTTL = 12 * time.Hour

const maxPEMSize = 256 << 10

type server struct {
	mu       sync.Mutex
	sessions map[string]*Session
	readonly bool

	// autoSpec 不为 nil 表示已通过环境变量配置了自动登录。
	autoSpec *ConnSpec
	autoSess *Session
	autoErr  error
	autoAt   time.Time
}

func (s *server) routes() http.Handler {
	staticFS, err := fs.Sub(webFS, "web/static")
	if err != nil {
		log.Fatalf("静态资源加载失败: %v", err)
	}

	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))

	mux.HandleFunc("GET /{$}", s.handleRoot)
	mux.HandleFunc("GET /login", s.handleLoginForm)
	mux.HandleFunc("POST /login", s.handleLogin)
	mux.HandleFunc("POST /logout", s.withAuth(s.handleLogout))

	mux.HandleFunc("GET /databases", s.withAuth(s.handleDatabases))
	mux.HandleFunc("POST /databases/create", s.withAuth(s.handleCreateDatabase))
	mux.HandleFunc("POST /databases/drop", s.withAuth(s.handleDropDatabase))

	mux.HandleFunc("GET /db/{db}/browse", s.withAuth(s.handleBrowse))
	mux.HandleFunc("GET /db/{db}/structure", s.withAuth(s.handleStructure))
	mux.HandleFunc("GET /db/{db}/sql", s.withAuth(s.handleSQLForm))
	mux.HandleFunc("POST /db/{db}/sql", s.withAuth(s.handleSQLExec))
	mux.HandleFunc("GET /db/{db}/sql/export", s.withAuth(s.handleSQLExport))
	mux.HandleFunc("POST /db/{db}/sql/favorite", s.withAuth(s.handleSQLFavorite))
	mux.HandleFunc("GET /db/{db}/export", s.withAuth(s.handleExportCSV))
	mux.HandleFunc("POST /db/{db}/table", s.withAuth(s.handleTableAction))
	mux.HandleFunc("GET /db/{db}/er", s.withAuth(s.handleER))
	mux.HandleFunc("GET /db/{db}/design", s.withAuth(s.handleDesign))
	mux.HandleFunc("POST /db/{db}/design/create", s.withAuth(s.handleDesignCreate))
	mux.HandleFunc("POST /db/{db}/design/alter", s.withAuth(s.handleDesignAlter))
	mux.HandleFunc("GET /db/{db}/backup", s.withAuth(s.handleBackupPage))
	mux.HandleFunc("POST /db/{db}/backup/dump", s.withAuth(s.handleBackupDump))
	mux.HandleFunc("POST /db/{db}/backup/restore", s.withAuth(s.handleBackupRestore))
	mux.HandleFunc("GET /db/{db}/ops", s.withAuth(s.handleOps))
	mux.HandleFunc("POST /db/{db}/ops", s.withAuth(s.handleOpsAction))
	mux.HandleFunc("GET /db/{db}/row", s.withAuth(s.handleRowForm))
	mux.HandleFunc("POST /db/{db}/row/save", s.withAuth(s.handleRowSave))
	mux.HandleFunc("POST /db/{db}/row/delete", s.withAuth(s.handleRowDelete))
	return mux
}

func (s *server) render(w http.ResponseWriter, name string, p *Page) {
	t, ok := templates[name]
	if !ok {
		http.Error(w, "模板不存在", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(w, "base", p); err != nil {
		log.Printf("渲染模板 %s 失败: %v", name, err)
	}
}

func (s *server) withAuth(h func(http.ResponseWriter, *http.Request, *Session)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess := s.sessionFrom(r)
		if sess == nil {
			auto, err := s.autoSession()
			if err != nil {
				http.Error(w, "自动登录失败:"+friendlyConnErr(err), http.StatusBadGateway)
				return
			}
			if auto == nil {
				http.Redirect(w, r, "/login", http.StatusSeeOther)
				return
			}
			sess = auto
		}
		if r.Method == http.MethodPost && !validCSRF(sess, r) {
			http.Error(w, "表单令牌校验失败,请返回刷新页面后重试", http.StatusForbidden)
			return
		}
		h(w, r, sess)
	}
}

// prepareConfig 在只读模式下把连接默认事务设为只读,由数据库兜底拒绝写入。
func (s *server) prepareConfig(cfg *pgx.ConnConfig) {
	if !s.readonly {
		return
	}
	if cfg.RuntimeParams == nil {
		cfg.RuntimeParams = map[string]string{}
	}
	cfg.RuntimeParams["default_transaction_read_only"] = "on"
}

// autoEnabled 表示已配置环境变量自动登录,无需登录页。
func (s *server) autoEnabled() bool {
	return s.autoSpec != nil
}

// autoSession 返回自动登录的共享会话,首次访问时按懒加载方式建立连接。
func (s *server) autoSession() (*Session, error) {
	s.mu.Lock()
	if s.autoSess != nil {
		sess := s.autoSess
		s.mu.Unlock()
		return sess, nil
	}
	spec := s.autoSpec
	if spec == nil {
		s.mu.Unlock()
		return nil, nil
	}
	if s.autoErr != nil && time.Since(s.autoAt) < 5*time.Second {
		err := s.autoErr
		s.mu.Unlock()
		return nil, err
	}
	s.mu.Unlock()

	cfg, err := buildConnConfig(*spec)
	if err == nil {
		s.prepareConfig(cfg)
	}
	var sess *Session
	if err == nil {
		sess = newSession(cfg, spec.Method)
		if _, err = sess.Pool(cfg.Database); err != nil {
			sess.Close()
			sess = nil
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		s.autoErr, s.autoAt = err, time.Now()
		return nil, err
	}
	if s.autoSess != nil {
		sess.Close()
		return s.autoSess, nil
	}
	s.autoSess = sess
	s.autoErr = nil
	return sess, nil
}

func validCSRF(sess *Session, r *http.Request) bool {
	tok := r.FormValue("_csrf")
	if tok == "" {
		tok = r.Header.Get("X-CSRF-Token")
	}
	return subtle.ConstantTimeCompare([]byte(tok), []byte(sess.CSRF)) == 1
}

func (s *server) addSession(w http.ResponseWriter, sess *Session) {
	s.mu.Lock()
	s.sessions[sess.ID] = sess
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    sess.ID,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(sessionTTL.Seconds()),
	})
}

func (s *server) sessionFrom(r *http.Request) *Session {
	c, err := r.Cookie(cookieName)
	if err != nil || c.Value == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := s.sessions[c.Value]
	if sess == nil {
		return nil
	}
	if time.Since(sess.LastSeen) > sessionTTL {
		delete(s.sessions, c.Value)
		go sess.Close()
		return nil
	}
	sess.LastSeen = time.Now()
	return sess
}

func (s *server) dropSession(w http.ResponseWriter, r *http.Request) *Session {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return nil
	}
	s.mu.Lock()
	sess := s.sessions[c.Value]
	delete(s.sessions, c.Value)
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", HttpOnly: true, MaxAge: -1})
	return sess
}

func (s *server) gc() {
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for range t.C {
		var stale []*Session
		s.mu.Lock()
		for id, sess := range s.sessions {
			if time.Since(sess.LastSeen) > sessionTTL {
				delete(s.sessions, id)
				stale = append(stale, sess)
			}
		}
		s.mu.Unlock()
		for _, sess := range stale {
			sess.Close()
		}
	}
}

func (s *server) closeAll() {
	s.mu.Lock()
	sessions := make([]*Session, 0, len(s.sessions)+1)
	for id, sess := range s.sessions {
		sessions = append(sessions, sess)
		delete(s.sessions, id)
	}
	if s.autoSess != nil {
		sessions = append(sessions, s.autoSess)
		s.autoSess = nil
	}
	s.mu.Unlock()
	for _, sess := range sessions {
		sess.Close()
	}
}

func randomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		log.Fatalf("生成随机令牌失败: %v", err)
	}
	return hex.EncodeToString(b)
}

func (s *server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if s.autoEnabled() || s.sessionFrom(r) != nil {
		http.Redirect(w, r, "/databases", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func defaultLoginPage(errMsg string) *Page {
	return &Page{
		Title:      "登录",
		AuthMethod: authPassword,
		FormHost:   "127.0.0.1",
		FormPort:   5432,
		FormUser:   "postgres",
		FormDB:     "postgres",
		FormSSL:    "disable",
		Err:        errMsg,
	}
}

func (s *server) handleLoginForm(w http.ResponseWriter, r *http.Request) {
	if s.autoEnabled() || s.sessionFrom(r) != nil {
		http.Redirect(w, r, "/databases", http.StatusSeeOther)
		return
	}
	s.render(w, "login", defaultLoginPage(""))
}

func (s *server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if s.autoEnabled() {
		http.Redirect(w, r, "/databases", http.StatusSeeOther)
		return
	}
	if err := r.ParseMultipartForm(16 << 20); err != nil && !errors.Is(err, http.ErrNotMultipart) {
		s.render(w, "login", defaultLoginPage("表单解析失败: "+err.Error()))
		return
	}

	port, _ := strconv.Atoi(strings.TrimSpace(r.FormValue("port")))
	if port <= 0 || port > 65535 {
		port = 5432
	}
	spec := ConnSpec{
		Method:   r.FormValue("method"),
		Host:     strings.TrimSpace(r.FormValue("host")),
		Port:     port,
		User:     strings.TrimSpace(r.FormValue("user")),
		Password: r.FormValue("password"),
		Database: strings.TrimSpace(r.FormValue("db")),
		SSLMode:  r.FormValue("sslmode"),
		Socket:   r.FormValue("socket") != "",
		DSN:      strings.TrimSpace(r.FormValue("dsn")),
	}
	if spec.Method == "" {
		spec.Method = authPassword
	}

	page := &Page{
		Title:      "登录",
		AuthMethod: spec.Method,
		FormHost:   spec.Host,
		FormPort:   spec.Port,
		FormUser:   spec.User,
		FormDB:     spec.Database,
		FormSSL:    spec.SSLMode,
		FormSocket: spec.Socket,
		FormDSN:    spec.DSN,
	}
	if page.FormHost == "" {
		page.FormHost = "127.0.0.1"
	}
	if page.FormSSL == "" {
		page.FormSSL = "disable"
	}

	if spec.Method == authCert {
		var err error
		if spec.CAPEM, err = readPEMFile(r, "ca"); err != nil {
			page.Err = err.Error()
			s.render(w, "login", page)
			return
		}
		if spec.CertPEM, err = readPEMFile(r, "cert"); err != nil {
			page.Err = err.Error()
			s.render(w, "login", page)
			return
		}
		if spec.KeyPEM, err = readPEMFile(r, "key"); err != nil {
			page.Err = err.Error()
			s.render(w, "login", page)
			return
		}
	}

	cfg, err := buildConnConfig(spec)
	if err != nil {
		page.Err = err.Error()
		s.render(w, "login", page)
		return
	}
	s.prepareConfig(cfg)

	sess := newSession(cfg, spec.Method)
	if _, err := sess.Pool(cfg.Database); err != nil {
		sess.Close()
		page.Err = friendlyConnErr(err)
		s.render(w, "login", page)
		return
	}
	s.addSession(w, sess)
	http.Redirect(w, r, "/databases", http.StatusSeeOther)
}

func readPEMFile(r *http.Request, field string) ([]byte, error) {
	f, _, err := r.FormFile(field)
	if errors.Is(err, http.ErrMissingFile) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, maxPEMSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxPEMSize {
		return nil, errors.New("证书文件过大(上限 256KB)")
	}
	return data, nil
}

func (s *server) handleLogout(w http.ResponseWriter, r *http.Request, sess *Session) {
	if old := s.dropSession(w, r); old != nil {
		old.Close()
	}
	if s.autoEnabled() {
		// 环境变量自动登录无法真正退出,清掉 Cookie 后回到管理台。
		http.Redirect(w, r, "/databases", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func friendlyConnErr(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "password authentication failed"):
		return "认证失败:用户名或密码不正确"
	case strings.Contains(msg, "no pg_hba.conf entry"):
		return "服务器拒绝了该连接的认证方式,请检查 pg_hba.conf:" + msg
	case strings.Contains(msg, "connection refused"):
		return "连接被拒绝,请确认 PostgreSQL 正在运行且监听该地址:" + msg
	case strings.Contains(msg, "certificate"):
		return "TLS/证书校验失败:" + msg
	default:
		return "连接失败:" + msg
	}
}
