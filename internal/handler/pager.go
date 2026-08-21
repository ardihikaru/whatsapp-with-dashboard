package handler

import (
	"net/url"
	"strconv"
)

// pagerView is the numbered table footer shared by the paged admin lists. It is
// built server-side so the templates stay logic-free.
type pagerView struct {
	Page, Pages, Total int
	From, To           int // 1-based row range shown on this page (0,0 = empty)
	Links              []pageLink
}

// pageLink is one entry in the pager strip. URL == "" renders as static text —
// either the current page or the "…" gap.
type pageLink struct {
	Label   string
	URL     string
	Current bool
	Gap     bool
}

// pagerWindow is how many numbered links surround the current page.
const pagerWindow = 2

// newPager computes the footer for page (1-based) of total rows at pageSize.
// base is the path and q the already-populated query string; the "page" key is
// overwritten per link so active filters survive paging.
func newPager(base string, q url.Values, page, pageSize, total int) pagerView {
	pages := (total + pageSize - 1) / pageSize
	if pages < 1 {
		pages = 1
	}
	if page < 1 {
		page = 1
	}
	if page > pages {
		page = pages
	}
	p := pagerView{Page: page, Pages: pages, Total: total}
	if total > 0 {
		p.From = (page-1)*pageSize + 1
		p.To = min(page*pageSize, total)
	}
	href := func(n int) string {
		v := url.Values{}
		for k, vals := range q {
			v[k] = vals
		}
		v.Set("page", strconv.Itoa(n))
		return base + "?" + v.Encode()
	}
	for n := 1; n <= pages; n++ {
		switch {
		case n == 1 || n == pages || (n >= page-pagerWindow && n <= page+pagerWindow):
			p.Links = append(p.Links, pageLink{
				Label: strconv.Itoa(n), URL: href(n), Current: n == page,
			})
		case n == page-pagerWindow-1 || n == page+pagerWindow+1:
			p.Links = append(p.Links, pageLink{Label: "…", Gap: true})
		}
	}
	return p
}

// pageParam reads a 1-based ?page, defaulting to 1 on anything unparseable.
func pageParam(raw string) int {
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return 1
	}
	return n
}
