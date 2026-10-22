package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// 支持的连接认证方式。
const (
	authPassword = "password"
	authTrust    = "trust"
	authCert     = "cert"
	authDSN      = "dsn"
)

type ConnSpec struct {
	Method   string
	Host     string
	Port     int
	User     string
	Password string
	Database string
	SSLMode  string
	Socket   bool
	DSN      string
	CAPEM    []byte
	CertPEM  []byte
	KeyPEM   []byte
}

func authLabel(method string) string {
	switch method {
	case authTrust:
		return "免密认证"
	case authCert:
		return "客户端证书"
	case authDSN:
		return "连接串"
	default:
		return "密码认证"
	}
}

// buildConnConfig 生成基础连接配置;Database 由连接池按目标库覆盖。
func buildConnConfig(spec ConnSpec) (*pgx.ConnConfig, error) {
	if spec.Method == "" {
		spec.Method = authPassword
	}
	if spec.Database == "" {
		spec.Database = "postgres"
	}

	var (
		pcfg *pgxpool.Config
		err  error
	)
	switch spec.Method {
	case authDSN:
		dsn := strings.TrimSpace(spec.DSN)
		if dsn == "" {
			return nil, errors.New("请填写连接串")
		}
		pcfg, err = pgxpool.ParseConfig(dsn)
		if err != nil {
			return nil, fmt.Errorf("连接串解析失败: %w", err)
		}
	case authPassword, authTrust, authCert:
		pcfg, err = parseParamConfig(spec)
		if err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("不支持的认证方式: %s", spec.Method)
	}

	cfg := pcfg.ConnConfig
	if cfg.Database == "" {
		cfg.Database = "postgres"
	}
	if spec.Method == authCert {
		if err := applyClientCert(cfg, spec); err != nil {
			return nil, err
		}
	}
	return cfg, nil
}

func parseParamConfig(spec ConnSpec) (*pgxpool.Config, error) {
	user := strings.TrimSpace(spec.User)
	if user == "" {
		return nil, errors.New("请填写数据库用户名")
	}
	port := spec.Port
	if port <= 0 || port > 65535 {
		port = 5432
	}
	sslmode := spec.SSLMode
	if sslmode == "" {
		sslmode = "disable"
	}

	host := strings.TrimSpace(spec.Host)
	if spec.Socket && !strings.HasPrefix(host, "/") {
		host = "/var/run/postgresql"
	}
	if host == "" {
		host = "127.0.0.1"
	}

	password := spec.Password
	if spec.Method == authTrust {
		password = ""
	}

	q := url.Values{}
	q.Set("sslmode", sslmode)

	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(user, password),
		Path:     "/" + spec.Database,
		RawQuery: q.Encode(),
	}
	if strings.HasPrefix(host, "/") {
		q.Set("host", host)
		q.Set("port", strconv.Itoa(port))
		u.RawQuery = q.Encode()
	} else {
		u.Host = net.JoinHostPort(host, strconv.Itoa(port))
	}

	pcfg, err := pgxpool.ParseConfig(u.String())
	if err != nil {
		return nil, fmt.Errorf("连接参数无效: %w", err)
	}
	return pcfg, nil
}

// applyClientCert 在 TLS 配置上挂载客户端证书;sslmode 必须启用 TLS。
func applyClientCert(cfg *pgx.ConnConfig, spec ConnSpec) error {
	if len(spec.CertPEM) == 0 || len(spec.KeyPEM) == 0 {
		return errors.New("客户端证书认证需要上传证书与私钥")
	}
	if cfg.TLSConfig == nil {
		return errors.New("客户端证书认证需要 TLS,请把 SSL 模式设为 require / verify-ca / verify-full")
	}
	cert, err := tls.X509KeyPair(spec.CertPEM, spec.KeyPEM)
	if err != nil {
		return fmt.Errorf("证书或私钥无效: %w", err)
	}
	if len(spec.CAPEM) > 0 {
		roots := cfg.TLSConfig.RootCAs
		if roots == nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM(spec.CAPEM) {
			return errors.New("CA 证书解析失败")
		}
		cfg.TLSConfig.RootCAs = roots
	}
	cfg.TLSConfig.Certificates = []tls.Certificate{cert}
	// 避免 sslmode=prefer 时静默退回明文。
	cfg.Fallbacks = nil
	return nil
}

// Session 保存一次登录的连接配置与按库缓存的连接池。
type Session struct {
	ID       string
	CSRF     string
	Method   string
	Host     string
	Port     uint16
	User     string
	Created  time.Time
	LastSeen time.Time

	base  *pgx.ConnConfig
	mu    sync.Mutex
	db    string
	pools map[string]*pgxpool.Pool

	history    []SQLHistory
	favorites  []SQLFavorite
	lastResult *storedResults
}

func newSession(cfg *pgx.ConnConfig, method string) *Session {
	now := time.Now()
	host := cfg.Host
	if host == "" {
		host = "localhost"
	}
	port := cfg.Port
	if port == 0 {
		port = 5432
	}
	user := cfg.User
	if user == "" {
		user = "-"
	}
	return &Session{
		ID:       randomToken(24),
		CSRF:     randomToken(24),
		Method:   method,
		Host:     host,
		Port:     port,
		User:     user,
		Created:  now,
		LastSeen: now,
		base:     cfg,
		db:       cfg.Database,
		pools:    map[string]*pgxpool.Pool{},
	}
}

func (s *Session) SetDB(db string) {
	s.mu.Lock()
	s.db = db
	s.mu.Unlock()
}

func (s *Session) DB() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db
}

// Pool 返回目标数据库的连接池,不存在则创建并探活。
func (s *Session) Pool(db string) (*pgxpool.Pool, error) {
	if db == "" {
		db = "postgres"
	}
	s.mu.Lock()
	if p, ok := s.pools[db]; ok {
		s.mu.Unlock()
		return p, nil
	}
	s.mu.Unlock()

	cfg := s.base.Copy()
	cfg.Database = db
	cfg.ConnectTimeout = 8 * time.Second

	pcfg, err := pgxpool.ParseConfig("postgres:///")
	if err != nil {
		return nil, err
	}
	pcfg.ConnConfig = cfg
	pcfg.MaxConns = 4
	pcfg.MaxConnIdleTime = 5 * time.Minute
	pcfg.MaxConnLifetime = time.Hour

	pool, err := pgxpool.NewWithConfig(context.Background(), pcfg)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.pools[db]; ok {
		pool.Close()
		return existing, nil
	}
	s.pools[db] = pool
	return pool, nil
}

func (s *Session) Close() {
	s.mu.Lock()
	pools := s.pools
	s.pools = map[string]*pgxpool.Pool{}
	s.mu.Unlock()
	for _, p := range pools {
		p.Close()
	}
}
