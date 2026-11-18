package controllers

import (
	"net/http"
	"sort"
	"strconv"
	"strings"

	contractshttp "github.com/goravel/framework/contracts/http"

	"rllmx/app/llm/quota"
	"rllmx/app/store/repository"
)

const (
	clientPageSize    = 20
	clientPageSizeMax = 500
	clientPageMax     = 1_000_000
)

// Client answers GET /api/providers/client: the connections a quota reading
// answers for, with the filters and pages the usage page asks for.
func (p *ProvidersController) Client(ctx contractshttp.Context) contractshttp.Response {
	st := p.store()
	if st == nil {
		return storageDown(ctx)
	}
	list, err := st.Connections().List(ctx.Request().Origin().Context(), repository.ConnFilter{})
	if err != nil {
		return flat(ctx, http.StatusInternalServerError, "Failed to fetch providers")
	}
	rows, names := eligibleClients(list)
	eligible := len(rows)

	provider := strings.TrimSpace(ctx.Request().Query("provider"))
	if provider == "" {
		provider = "all"
	}
	if provider != "all" {
		kept := rows[:0]
		for _, conn := range rows {
			if conn.Provider == provider {
				kept = append(kept, conn)
			}
		}
		rows = kept
	}
	filtered := len(rows)

	switch strings.TrimSpace(ctx.Request().Query("accountStatus")) {
	case "active":
		rows = keepStatus(rows, true)
	case "inactive":
		rows = keepStatus(rows, false)
	}

	if ctx.Request().Query("sort") == "provider" {
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].Provider < rows[j].Provider })
	} else {
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].Priority < rows[j].Priority })
	}

	page, size := clientPaging(ctx.Request().Query("page"), ctx.Request().Query("pageSize"))
	total := len(rows)
	from, to, pages := pageSpan(total, page, size)
	out := make([]map[string]any, 0, to-from)
	for _, conn := range rows[from:to] {
		out = append(out, connectionView(conn))
	}
	return ctx.Response().Json(http.StatusOK, contractshttp.Json{
		"connections":     out,
		"providerOptions": names,
		"pagination": contractshttp.Json{
			"page": page, "pageSize": size, "total": total, "totalPages": pages,
		},
		"totals": contractshttp.Json{
			"eligibleConnections":         eligible,
			"providerFilteredConnections": filtered,
		},
	})
}

// eligibleClients keeps the connections a quota reading answers for, and names
// the providers they belong to, sorted.
func eligibleClients(list []repository.Connection) ([]repository.Connection, []string) {
	var rows []repository.Connection
	seen := map[string]bool{}
	var names []string
	for _, conn := range list {
		if !quota.Serves(conn.Provider, conn.AuthType) {
			continue
		}
		if !seen[conn.Provider] {
			seen[conn.Provider] = true
			names = append(names, conn.Provider)
		}
		rows = append(rows, conn)
	}
	sort.Strings(names)
	if names == nil {
		names = []string{}
	}
	return rows, names
}

// pageSpan is the slice bounds of one page of total rows, and how many pages
// there are. A page past the end is an empty one at the end.
func pageSpan(total, page, size int) (from, to, pages int) {
	pages = (total + size - 1) / size
	from = min((page-1)*size, total)
	to = min(from+size, total)
	return from, to, pages
}

// keepStatus keeps the rows a dashboard filter asks for. A row that never stored
// its flag counts as active, as the reference treats it.
func keepStatus(rows []repository.Connection, active bool) []repository.Connection {
	kept := rows[:0]
	for _, conn := range rows {
		if conn.IsActive == active {
			kept = append(kept, conn)
		}
	}
	return kept
}

// clientPaging reads the page and its size, both bounded, so the offset
// computed from them cannot wrap.
func clientPaging(page, size string) (int, int) {
	return intParam(page, 1, clientPageMax), intParam(size, clientPageSize, clientPageSizeMax)
}

// intParam reads a positive whole number from a query value: def when it is
// empty, not a number or below one, and never more than max.
func intParam(raw string, def, max int) int {
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return def
	}
	return min(n, max)
}
