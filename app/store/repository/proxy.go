package repository

import (
	contractsdb "github.com/goravel/framework/contracts/database/db"

	"rllmx/app/store/dialect"
	"rllmx/app/store/document"
)

// proxyKeys are the reference's per-connection proxy settings. This router
// dials direct, so a database it opens keeps them only until the next start.
var proxyKeys = []string{
	"connectionProxyEnabled",
	"connectionProxyUrl",
	"connectionNoProxy",
	"connectionProxyPoolId",
	"proxyPoolId",
}

// StripProxyKeys removes them from a connection document's providerSpecificData
// and reports whether any were there. A providerSpecificData that is not an
// object is left alone, and every other key keeps its bytes and position.
func StripProxyKeys(doc string) (string, bool, error) {
	d, err := document.ParseDoc(doc)
	if err != nil {
		return doc, false, err
	}
	raw, ok := d.GetRaw("providerSpecificData")
	if !ok {
		return doc, false, nil
	}
	psd, err := document.ParseDoc(raw)
	if err != nil {
		return doc, false, nil
	}
	changed := false
	for _, key := range proxyKeys {
		if !psd.Has(key) {
			continue
		}
		if err := psd.Delete(key); err != nil {
			return doc, false, err
		}
		changed = true
	}
	if !changed {
		return doc, false, nil
	}
	if err := d.SetRaw("providerSpecificData", psd.String()); err != nil {
		return doc, false, err
	}
	return d.String(), true, nil
}

// stripProxyKeys removes the proxy settings from every connection that carries
// them. With apply it writes those rows; a row whose JSON does not parse is left
// for the secret scan to report.
func (s *Store) stripProxyKeys(x contractsdb.Tx, apply bool) (int, error) {
	rows, err := s.readPairs(x, "providerConnections", selectConns)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, row := range rows {
		out, changed, err := StripProxyKeys(row.V)
		if err != nil || !changed {
			continue
		}
		n++
		if apply {
			if err := s.stmt(x, `UPDATE "providerConnections" SET "data"=? WHERE "id"=?`, dialect.StripNUL(out), row.ID); err != nil {
				return 0, err
			}
		}
	}
	return n, nil
}
