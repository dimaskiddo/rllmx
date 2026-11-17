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
	var rows []repository.Connection
	options := map[string]bool{}
	for _, conn := range list {
		if !quota.Serves(conn.Provider, conn.AuthType) {
			continue
		}
		options[conn.Provider] = true
		rows = append(rows, conn)
	}
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
	pages := (total + size - 1) / size
	from := (page - 1) * size
	if from > total {
		from = total
	}
	to := from + size
	if to > total {
		to = total
	}

	out := make([]map[string]any, 0, to-from)
	for _, conn := range rows[from:to] {
		out = append(out, connectionView(conn))
	}
	names := make([]string, 0, len(options))
	for name := range options {
		names = append(names, name)
	}
	sort.Strings(names)
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

// clientPaging reads the page and its size, both bounded.
func clientPaging(page, size string) (int, int) {
	p, err := strconv.Atoi(page)
	if err != nil || p < 1 {
		p = 1
	}
	n, err := strconv.Atoi(size)
	if err != nil || n < 1 {
		n = clientPageSize
	}
	if n > clientPageSizeMax {
		n = clientPageSizeMax
	}
	return p, n
}
