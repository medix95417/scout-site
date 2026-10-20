package web

import (
	"net/url"
	"strconv"
)

// pager is one page of a list shown a few items at a time: which slice of the
// list to show, and the links to its neighbours.
//
// It is for public listings that are plain links, so a page is a URL a visitor
// can bookmark or share and a search engine can follow — nothing here needs
// JavaScript. The admin screens that load "25 more" in place are a different
// thing, with their own reasons.
type pager struct {
	Page, Pages int
	// Start and End bound the slice of the list this page shows: items[Start:End].
	Start, End int
	// PrevURL and NextURL are empty at either end. The first page is the bare
	// path, so the address a visitor already has is still the first page.
	PrevURL, NextURL string
}

// newPager pages a list of total items, size to a page, for the "page" value a
// request arrived with.
//
// A page that is missing, not a number, or below 1 is the first; one past the
// end is the last. A visitor with a stale link to page 9 of a list that has
// since shrunk to 4 pages is better served by the last page than by an error,
// and a hand-edited value is never an error worth reporting.
func newPager(total, size int, rawPage, path string) pager {
	if size < 1 {
		size = 1
	}
	pages := (total + size - 1) / size
	if pages < 1 {
		pages = 1
	}
	page, err := strconv.Atoi(rawPage)
	if err != nil || page < 1 {
		page = 1
	}
	if page > pages {
		page = pages
	}

	p := pager{Page: page, Pages: pages, Start: (page - 1) * size}
	p.End = p.Start + size
	if p.End > total {
		p.End = total
	}
	if page > 1 {
		p.PrevURL = pageURL(path, page-1)
	}
	if page < pages {
		p.NextURL = pageURL(path, page+1)
	}
	return p
}

func pageURL(path string, page int) string {
	if page <= 1 {
		return path
	}
	return path + "?" + url.Values{"page": {strconv.Itoa(page)}}.Encode()
}
