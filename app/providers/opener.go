package providers

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/goravel/framework/contracts/config"
	contractsdriver "github.com/goravel/framework/contracts/database/driver"
	"github.com/goravel/framework/contracts/log"
	"github.com/goravel/framework/database/db"
	ormbuild "github.com/goravel/framework/database/orm"
	schemabuild "github.com/goravel/framework/database/schema"
	"github.com/goravel/sqlite"
	sqlitefacades "github.com/goravel/sqlite/facades"

	"rllmx/app/platform/file"
	"rllmx/app/store/repository"
)

var openerSeq atomic.Int64

// SQLiteOpener opens one SQLite file per call under its own connection name.
// The framework caches a built connection by name for the whole process, so a
// shared name would hand every caller the first file it opened. Closing the
// pool is the caller's job, through Conn.Close. The provider and the db
// commands both open legacy copies with it.
func SQLiteOpener(cfg config.Config, l log.Log) repository.Opener {
	return func(path, query string) (*repository.Conn, error) {
		conn := fmt.Sprintf("rllmx_extra_%d", openerSeq.Add(1))
		key := "database.connections." + conn
		cfg.Add(key, map[string]any{
			"driver":   sqlite.Name,
			"database": path,
			"dsn":      file.URI(path) + "?" + query,
			"prefix":   "",
			"singular": false,
			"via": func() (contractsdriver.Driver, error) {
				return sqlitefacades.Sqlite(conn)
			},
		})
		ctx := context.Background()
		handle, err := db.BuildDB(ctx, cfg, l, conn, nil)
		if err != nil {
			return nil, err
		}
		orm, err := ormbuild.BuildOrm(ctx, cfg, conn, l, nil, nil)
		if err != nil {
			return nil, err
		}
		sch, err := schemabuild.NewSchema(cfg, l, orm, sqlite.NewSqlite(cfg, l, conn), nil)
		if err != nil {
			return nil, err
		}
		return &repository.Conn{DB: handle, Schema: sch, Close: func() error {
			pool, err := orm.DB()
			if err != nil {
				return err
			}
			return pool.Close()
		}}, nil
	}
}
