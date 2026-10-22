package main

import (
	"net/url"
	"strconv"
)

// buildBrowseURL 生成保留筛选、隐藏列与排序的浏览页链接。
func (p *Page) buildBrowseURL(size, page int, sort, dir string) string {
	q := url.Values{}
	q.Set("schema", p.CurSchema)
	q.Set("table", p.CurTable)
	if size > 0 {
		q.Set("size", strconv.Itoa(size))
	}
	if page > 0 {
		q.Set("page", strconv.Itoa(page))
	}
	if sort != "" {
		q.Set("sort", sort)
	}
	if dir != "" {
		q.Set("dir", dir)
	}
	for _, f := range p.Filters {
		q.Add("f.col", f.Column)
		q.Add("f.op", f.Op)
		q.Add("f.val", f.Value)
	}
	for _, h := range p.HiddenList {
		q.Add("hide", h)
	}
	return "/db/" + url.PathEscape(p.CurDB) + "/browse?" + q.Encode()
}

func (p *Page) SizeURL(n int) string {
	return p.buildBrowseURL(n, 1, p.Sort, p.Dir)
}

func (p *Page) PageURL(n int) string {
	return p.buildBrowseURL(p.Size, n, p.Sort, p.Dir)
}

func (p *Page) SortURL(col string) string {
	dir := "asc"
	if p.Sort == col && p.Dir == "asc" {
		dir = "desc"
	}
	return p.buildBrowseURL(p.Size, 1, col, dir)
}

func (p *Page) ExportURL() string {
	q := url.Values{}
	q.Set("schema", p.CurSchema)
	q.Set("table", p.CurTable)
	q.Set("sort", p.Sort)
	q.Set("dir", p.Dir)
	for _, f := range p.Filters {
		q.Add("f.col", f.Column)
		q.Add("f.op", f.Op)
		q.Add("f.val", f.Value)
	}
	for _, h := range p.HiddenList {
		q.Add("hide", h)
	}
	return "/db/" + url.PathEscape(p.CurDB) + "/export?" + q.Encode()
}
