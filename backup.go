package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// BackupView 描述备份页的工具可用性与表清单。
type BackupView struct {
	PgDump    string
	PgRestore string
	Psql      string
	Reason    string
	Tables    []TableRef
	Schemas   []string
}

const maxRestoreSize = 64 << 20

func (s *server) handleBackupPage(w http.ResponseWriter, r *http.Request, sess *Session) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	db := r.PathValue("db")
	p := s.newPage(sess, "备份与恢复")
	p.CurDB = db
	p.Flash = r.URL.Query().Get("msg")
	s.fillNav(ctx, sess, p, db)

	view := &BackupView{}
	view.PgDump, _ = exec.LookPath("pg_dump")
	view.PgRestore, _ = exec.LookPath("pg_restore")
	view.Psql, _ = exec.LookPath("psql")
	if sess.Method == authCert {
		view.Reason = "客户端证书认证暂不支持命令行备份,请改用密码或连接串登录"
	}
	if view.PgDump == "" {
		view.Reason = "未找到 pg_dump,请安装 postgresql-client"
	}
	for _, schema := range p.Schemas {
		view.Schemas = append(view.Schemas, schema.Name)
		for _, t := range schema.Tables {
			view.Tables = append(view.Tables, t)
		}
	}
	p.Backup = view
	s.render(w, "backup", p)
}

// dumpCommand 组装 pg_dump 调用所需的参数与环境变量。
func (s *server) dumpCommand(sess *Session, db, format, schema, table string, schemaOnly bool) (*exec.Cmd, error) {
	if sess.Method == authCert {
		return nil, fmt.Errorf("客户端证书认证暂不支持 pg_dump")
	}
	bin := "pg_dump"
	args := []string{
		"-h", sess.Host,
		"-p", itoa(int(sess.Port)),
		"-U", sess.User,
		"-d", db,
		"--no-password",
	}
	switch format {
	case "custom":
		args = append(args, "-F", "c")
	case "tar":
		args = append(args, "-F", "t")
	default:
		args = append(args, "-F", "p")
	}
	if schemaOnly {
		args = append(args, "--schema-only")
	}
	if table != "" {
		args = append(args, "-t", quoteIdent(schema, table))
	}
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(),
		"PGPASSWORD="+sess.base.Password,
		"PGSSLMODE="+sslMode(sess.base),
		"PGCONNECT_TIMEOUT=10",
		"PGAPPNAME=pggo",
	)
	return cmd, nil
}

// sslMode 为命令行工具选择宽松的 SSL 模式:TLS 可用时优先加密。
func sslMode(cfg *pgx.ConnConfig) string {
	if cfg != nil && cfg.TLSConfig != nil {
		return "prefer"
	}
	return "disable"
}

func (s *server) handleBackupDump(w http.ResponseWriter, r *http.Request, sess *Session) {
	db := r.PathValue("db")
	format := r.FormValue("format")
	schema := r.FormValue("schema")
	table := r.FormValue("table")
	schemaOnly := r.FormValue("schema_only") != ""

	if schema == "" {
		schema = "public"
	}
	if table != "" {
		if _, err := relationName(schema, table); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}

	cmd, err := s.dumpCommand(sess, db, format, schema, table, schemaOnly)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if _, err := exec.LookPath("pg_dump"); err != nil {
		http.Error(w, "未找到 pg_dump,请安装 postgresql-client", http.StatusNotImplemented)
		return
	}

	tmp, err := os.CreateTemp("", "pggo-dump-*")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()

	var stderr bytes.Buffer
	cmd.Stdout = tmp
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		http.Error(w, "pg_dump 失败:"+strings.TrimSpace(stderr.String()), http.StatusInternalServerError)
		return
	}

	ext := ".sql"
	switch format {
	case "custom":
		ext = ".dump"
	case "tar":
		ext = ".tar"
	}
	filename := fmt.Sprintf("%s_%s%s", db, time.Now().Format("20060102_150405"), ext)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	http.ServeFile(w, r, tmp.Name())
}

func (s *server) handleBackupRestore(w http.ResponseWriter, r *http.Request, sess *Session) {
	if s.readonly {
		http.Error(w, "只读模式下不允许恢复数据", http.StatusForbidden)
		return
	}
	db := r.PathValue("db")
	back := "/db/" + url.PathEscape(db) + "/backup"
	msg := func(text string) {
		http.Redirect(w, r, back+"?msg="+url.QueryEscape(text), http.StatusSeeOther)
	}

	if err := r.ParseMultipartForm(maxRestoreSize); err != nil {
		msg("上传失败:" + err.Error())
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		msg("请选择要恢复的文件")
		return
	}
	defer file.Close()

	format := r.FormValue("format")
	if format == "" || format == "auto" {
		switch strings.ToLower(filepath.Ext(header.Filename)) {
		case ".dump", ".backup":
			format = "custom"
		case ".tar":
			format = "tar"
		default:
			format = "plain"
		}
	}

	tmp, err := os.CreateTemp("", "pggo-restore-*")
	if err != nil {
		msg(err.Error())
		return
	}
	defer os.Remove(tmp.Name())
	written, err := io.Copy(tmp, io.LimitReader(file, maxRestoreSize+1))
	if err != nil {
		tmp.Close()
		msg("写入临时文件失败:" + err.Error())
		return
	}
	if written > maxRestoreSize {
		tmp.Close()
		msg("文件过大,上限 64MB")
		return
	}
	tmp.Close()

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Minute)
	defer cancel()

	env := append(os.Environ(),
		"PGPASSWORD="+sess.base.Password,
		"PGSSLMODE="+sslMode(sess.base),
		"PGCONNECT_TIMEOUT=10",
		"PGAPPNAME=pggo",
	)

	switch format {
	case "plain":
		psql, err := exec.LookPath("psql")
		if err == nil {
			args := []string{
				"-h", sess.Host,
				"-p", itoa(int(sess.Port)),
				"-U", sess.User,
				"-d", db,
				"-v", "ON_ERROR_STOP=1",
				"-q",
				"-f", tmp.Name(),
			}
			cmd := exec.CommandContext(ctx, psql, args...)
			cmd.Env = env
			var out bytes.Buffer
			cmd.Stdout = &out
			cmd.Stderr = &out
			if err := cmd.Run(); err != nil {
				msg("恢复失败:" + firstLines(out.String(), 400))
				return
			}
			msg("恢复完成(psql)")
			return
		}
		pool, err := sess.Pool(db)
		if err != nil {
			msg(err.Error())
			return
		}
		data, err := os.ReadFile(tmp.Name())
		if err != nil {
			msg(err.Error())
			return
		}
		sets, tag, err := runSQL(ctx, pool, string(data))
		_ = sets
		if err != nil {
			msg("恢复失败:" + err.Error())
			return
		}
		msg("恢复完成(" + tag + ")")
	case "custom", "tar":
		bin, err := exec.LookPath("pg_restore")
		if err != nil {
			msg("未找到 pg_restore,请安装 postgresql-client")
			return
		}
		args := []string{
			"-h", sess.Host,
			"-p", itoa(int(sess.Port)),
			"-U", sess.User,
			"-d", db,
			"--no-password",
			"--exit-on-error",
		}
		if r.FormValue("clean") != "" {
			args = append(args, "--clean", "--if-exists")
		}
		args = append(args, tmp.Name())
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Env = env
		var out bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &out
		if err := cmd.Run(); err != nil {
			msg("恢复失败:" + firstLines(out.String(), 400))
			return
		}
		msg("恢复完成(pg_restore)")
	default:
		msg("不支持的格式:" + format)
	}
}

func firstLines(s string, limit int) string {
	s = strings.TrimSpace(s)
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "…"
}
