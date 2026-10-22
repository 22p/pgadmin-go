package main

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

type ERColumn struct {
	Name string
	Type string
	PK   bool
	Y    int
}

type ERNode struct {
	Name    string
	X, Y    int
	W, H    int
	Columns []ERColumn
}

type EREdge struct {
	Path string
}

type ERView struct {
	Schema     string
	Schemas    []string
	Nodes      []ERNode
	Edges      []EREdge
	Width      int
	Height     int
	TableCount int
}

const (
	erNodeW     = 240
	erGapX      = 64
	erGapY      = 48
	erMargin    = 28
	erHeaderH   = 30
	erRowH      = 20
	maxERTables = 24
)

func (s *server) handleER(w http.ResponseWriter, r *http.Request, sess *Session) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	db := r.PathValue("db")
	p := s.newPage(sess, "ER 图")
	p.CurDB = db
	p.Flash = r.URL.Query().Get("msg")
	p.CurSchema = r.URL.Query().Get("schema")
	if p.CurSchema == "" {
		p.CurSchema = "public"
	}
	s.fillNav(ctx, sess, p, db)

	view := &ERView{Schema: p.CurSchema}
	for _, schema := range p.Schemas {
		view.Schemas = append(view.Schemas, schema.Name)
	}
	p.ER = view

	pool, err := sess.Pool(db)
	if err != nil {
		p.Err = err.Error()
		s.render(w, "er", p)
		return
	}

	colRows, err := pool.Query(ctx, `
SELECT c.relname, a.attname, format_type(a.atttypid, a.atttypmod),
       EXISTS (SELECT 1 FROM pg_index i WHERE i.indrelid = c.oid AND i.indisprimary AND a.attnum = ANY (i.indkey))
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
JOIN pg_attribute a ON a.attrelid = c.oid
WHERE n.nspname = $1 AND c.relkind IN ('r', 'p') AND a.attnum > 0 AND NOT a.attisdropped
ORDER BY c.relname, a.attnum
LIMIT 600`, p.CurSchema)
	if err != nil {
		p.Err = err.Error()
		s.render(w, "er", p)
		return
	}
	defer colRows.Close()

	order := []string{}
	cols := map[string][]ERColumn{}
	for colRows.Next() {
		var table, name, typ string
		var pk bool
		if err := colRows.Scan(&table, &name, &typ, &pk); err != nil {
			p.Err = err.Error()
			s.render(w, "er", p)
			return
		}
		if _, ok := cols[table]; !ok {
			if len(order) >= maxERTables {
				continue
			}
			order = append(order, table)
		}
		cols[table] = append(cols[table], ERColumn{Name: name, Type: typ, PK: pk})
	}

	fkRows, err := pool.Query(ctx, `
SELECT src.relname, dst.relname
FROM pg_constraint con
JOIN pg_class src ON src.oid = con.conrelid
JOIN pg_class dst ON dst.oid = con.confrelid
JOIN pg_namespace n ON n.oid = src.relnamespace
WHERE con.contype = 'f' AND n.nspname = $1`, p.CurSchema)
	var edges [][2]string
	if err == nil {
		defer fkRows.Close()
		for fkRows.Next() {
			var from, to string
			if err := fkRows.Scan(&from, &to); err != nil {
				break
			}
			edges = append(edges, [2]string{from, to})
		}
	}

	// 布局:最多 4 列网格,行高取该行最高节点。
	n := len(order)
	perRow := 4
	switch {
	case n <= 2:
		perRow = n
	case n <= 8:
		perRow = 3
	}
	if perRow < 1 {
		perRow = 1
	}
	rows := (n + perRow - 1) / perRow
	if rows < 1 {
		rows = 1
	}

	heights := make([]int, rows)
	nodeHeights := make([]int, len(order))
	for i, name := range order {
		h := erHeaderH + erRowH*len(cols[name]) + 10
		nodeHeights[i] = h
		if h > heights[i/perRow] {
			heights[i/perRow] = h
		}
	}

	nodeByTable := map[string]*ERNode{}
	y := erMargin
	for i, name := range order {
		row := i / perRow
		col := i % perRow
		if col == 0 && i > 0 {
			y += heights[row-1] + erGapY
		}
		node := ERNode{
			Name: name,
			X:    erMargin + col*(erNodeW+erGapX),
			Y:    y,
			W:    erNodeW,
			H:    nodeHeights[i],
		}
		for j, c := range cols[name] {
			c.Y = y + erHeaderH + 14 + j*erRowH
			if len([]rune(c.Name)) > 26 {
				c.Name = string([]rune(c.Name)[:26]) + "…"
			}
			node.Columns = append(node.Columns, c)
		}
		view.Nodes = append(view.Nodes, node)
	}
	for i := range view.Nodes {
		nodeByTable[view.Nodes[i].Name] = &view.Nodes[i]
	}

	for _, e := range edges {
		from, to := nodeByTable[e[0]], nodeByTable[e[1]]
		if from == nil || to == nil || from == to {
			continue
		}
		x1, y1 := from.X+from.W, from.Y+erHeaderH/2
		x2, y2 := to.X, to.Y+erHeaderH/2
		dx := 60 + absInt(x2-x1)/4
		view.Edges = append(view.Edges, EREdge{Path: fmt.Sprintf("M %d %d C %d %d, %d %d, %d %d", x1, y1, x1+dx, y1, x2-dx, y2, x2, y2)})
	}

	view.TableCount = n
	view.Width = erMargin*2 + perRow*erNodeW + (perRow-1)*erGapX
	if view.Width < 560 {
		view.Width = 560
	}
	view.Height = y + heights[rows-1] + erMargin
	if view.Height < 220 {
		view.Height = 220
	}
	s.render(w, "er", p)
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
