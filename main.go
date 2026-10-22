package main

import (
	"embed"
	"flag"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

//go:embed web
var webFS embed.FS

// version 由构建参数注入: -ldflags "-X main.version=..."
var version = "dev"

var templates map[string]*template.Template

func loadTemplates() error {
	pages := []string{"login", "databases", "browse", "structure", "sql", "edit", "ops", "backup", "design", "er"}
	funcs := template.FuncMap{
		"add1": func(i int, base int64) int64 { return base + int64(i) + 1 },
		"add":  func(a, b int) int { return a + b },
		"sub":  func(a, b int) int { return a - b },
		"splitTable": func(name string) []string {
			if i := strings.IndexByte(name, '.'); i > 0 {
				return []string{name[:i], name[i+1:]}
			}
			return []string{"public", name}
		},
	}
	templates = make(map[string]*template.Template, len(pages))
	for _, p := range pages {
		files := []string{"web/icons.html", "web/base.html", "web/" + p + ".html"}
		if p == "login" {
			// 登录页使用独立布局,不需要应用外壳。
			files = []string{"web/icons.html", "web/login.html"}
		}
		t, err := template.New("base").Funcs(funcs).ParseFS(webFS, files...)
		if err != nil {
			return err
		}
		templates[p] = t
	}
	return nil
}

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "HTTP 监听地址")
	showVersion := flag.Bool("version", false, "打印版本号并退出")
	readonly := flag.Bool("readonly", false, "只读模式:连接默认只读事务,并隐藏所有写入入口")
	flag.Parse()

	if *showVersion {
		fmt.Printf("pggo %s\n", version)
		return
	}

	if err := loadTemplates(); err != nil {
		log.Fatalf("加载模板失败: %v", err)
	}

	spec, err := autoLoginSpec()
	if err != nil {
		log.Fatalf("自动登录配置错误: %v", err)
	}
	s := &server{
		sessions: make(map[string]*Session),
		autoSpec: spec,
		readonly: *readonly || truthy(os.Getenv("PGGO_READONLY")),
	}
	if s.readonly {
		log.Printf("只读模式已开启:数据库连接将使用只读事务")
	}
	if spec != nil {
		log.Printf("已启用环境变量自动登录(%s),访问首页将直接进入管理台", authLabel(spec.Method))
	}
	srv := &http.Server{
		Addr:              *addr,
		Handler:           s.routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go s.gc()
	go func() {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
		<-ch
		s.closeAll()
		srv.Close()
	}()

	log.Printf("GoPgAdmin %s 已启动: http://%s", version, *addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("HTTP 服务异常: %v", err)
	}
}
