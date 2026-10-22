package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Activity struct {
	PID      int32
	User     string
	DB       string
	State    string
	Wait     string
	Started  string
	Elapsed  string
	Query    string
	Client   string
	Blocking string
}

type LockInfo struct {
	PID      int32
	User     string
	Relation string
	Mode     string
	Granted  bool
}

type TableStat struct {
	Name       string
	Size       string
	Live       int64
	SeqScan    int64
	IdxScan    int64
	LastVacuum string
}

type OpsView struct {
	Version    string
	Size       string
	Active     int
	Idle       int
	MaxConns   int
	HitRatio   string
	Activities []Activity
	Locks      []LockInfo
	TopTables  []TableStat
}

func (s *server) handleOps(w http.ResponseWriter, r *http.Request, sess *Session) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	db := r.PathValue("db")
	p := s.newPage(sess, "运维")
	p.Flash = r.URL.Query().Get("msg")
	p.CurDB = db
	s.fillNav(ctx, sess, p, db)

	pool, err := sess.Pool(db)
	if err != nil {
		p.Err = err.Error()
		s.render(w, "ops", p)
		return
	}

	view := &OpsView{}
	row := pool.QueryRow(ctx, `
SELECT current_setting('server_version'),
       pg_size_pretty(pg_database_size(current_database())),
       (SELECT count(*) FROM pg_stat_activity WHERE state = 'active'),
       (SELECT count(*) FROM pg_stat_activity WHERE state = 'idle'),
       current_setting('max_connections')::int,
       COALESCE((SELECT round(100.0 * sum(blks_hit) / NULLIF(sum(blks_hit) + sum(blks_read), 0), 2)::text
                 FROM pg_stat_database WHERE datname = current_database()), '-')
`)
	if err := row.Scan(&view.Version, &view.Size, &view.Active, &view.Idle, &view.MaxConns, &view.HitRatio); err != nil {
		p.Err = err.Error()
	}

	actRows, err := pool.Query(ctx, `
SELECT pid,
       COALESCE(usename, ''),
       COALESCE(datname, ''),
       COALESCE(state, ''),
       COALESCE(wait_event_type, '') || COALESCE(':' || wait_event, ''),
       COALESCE(to_char(query_start, 'HH24:MI:SS'), ''),
       COALESCE(round(extract(epoch FROM now() - query_start)::numeric, 1)::text, ''),
       left(regexp_replace(COALESCE(query, ''), '\s+', ' ', 'g'), 240),
       COALESCE(client_addr::text, 'local'),
       COALESCE(array_to_string(pg_blocking_pids(pid), ','), '')
FROM pg_stat_activity
WHERE backend_type = 'client backend' AND pid <> pg_backend_pid()
ORDER BY (state = 'active') DESC, query_start NULLS LAST
LIMIT 50`)
	if err != nil {
		p.Err = err.Error()
	} else {
		defer actRows.Close()
		for actRows.Next() {
			var a Activity
			if err := actRows.Scan(&a.PID, &a.User, &a.DB, &a.State, &a.Wait, &a.Started, &a.Elapsed, &a.Query, &a.Client, &a.Blocking); err != nil {
				p.Err = err.Error()
				break
			}
			view.Activities = append(view.Activities, a)
		}
	}

	lockRows, err := pool.Query(ctx, `
SELECT a.pid,
       COALESCE(a.usename, ''),
       COALESCE(l.relation::regclass::text, '-'),
       l.mode,
       l.granted
FROM pg_locks l
JOIN pg_stat_activity a ON a.pid = l.pid
WHERE NOT l.granted OR cardinality(pg_blocking_pids(a.pid)) > 0
ORDER BY l.granted, a.pid
LIMIT 50`)
	if err == nil {
		defer lockRows.Close()
		for lockRows.Next() {
			var l LockInfo
			if err := lockRows.Scan(&l.PID, &l.User, &l.Relation, &l.Mode, &l.Granted); err != nil {
				p.Err = err.Error()
				break
			}
			view.Locks = append(view.Locks, l)
		}
	}

	tableRows, err := pool.Query(ctx, `
SELECT schemaname || '.' || relname,
       pg_size_pretty(pg_total_relation_size(relid)),
       n_live_tup,
       seq_scan,
       COALESCE(idx_scan, 0),
       COALESCE(to_char(last_vacuum, 'MM-DD HH24:MI'), '—')
FROM pg_stat_user_tables
ORDER BY pg_total_relation_size(relid) DESC
LIMIT 10`)
	if err == nil {
		defer tableRows.Close()
		for tableRows.Next() {
			var t TableStat
			if err := tableRows.Scan(&t.Name, &t.Size, &t.Live, &t.SeqScan, &t.IdxScan, &t.LastVacuum); err != nil {
				p.Err = err.Error()
				break
			}
			view.TopTables = append(view.TopTables, t)
		}
	}

	p.Ops = view
	s.render(w, "ops", p)
}

func (s *server) handleOpsAction(w http.ResponseWriter, r *http.Request, sess *Session) {
	if s.readonly {
		http.Error(w, "只读模式下不允许运维操作", http.StatusForbidden)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
	defer cancel()

	db := r.PathValue("db")
	back := "/db/" + url.PathEscape(db) + "/ops"
	msg := func(text string) {
		http.Redirect(w, r, back+"?msg="+url.QueryEscape(text), http.StatusSeeOther)
	}

	pool, err := sess.Pool(db)
	if err != nil {
		msg(err.Error())
		return
	}

	switch r.FormValue("op") {
	case "cancel", "terminate":
		pid, err := strconv.Atoi(r.FormValue("pid"))
		if err != nil || pid <= 0 {
			msg("pid 无效")
			return
		}
		fn := "pg_cancel_backend"
		if r.FormValue("op") == "terminate" {
			fn = "pg_terminate_backend"
		}
		var ok bool
		if err := pool.QueryRow(ctx, "SELECT "+fn+"($1)", pid).Scan(&ok); err != nil {
			msg(err.Error())
			return
		}
		if ok {
			msg(fmt.Sprintf("%s(%d) 已执行", fn, pid))
		} else {
			msg("操作未生效,可能权限不足或会话已结束")
		}
	case "analyze", "vacuum":
		schema := r.FormValue("schema")
		table := r.FormValue("table")
		rel, err := relationName(schema, table)
		if err != nil {
			msg(err.Error())
			return
		}
		stmt := "VACUUM (ANALYZE) " + rel
		if r.FormValue("op") == "analyze" {
			stmt = "ANALYZE " + rel
		}
		if _, err := pool.Exec(ctx, stmt); err != nil {
			msg(strings.ToUpper(r.FormValue("op")) + " 失败:" + err.Error())
			return
		}
		msg(stmt + " 已执行")
	default:
		http.Error(w, "未知操作", http.StatusBadRequest)
	}
}
