package tests

import (
	"testing"

	contractsdriver "github.com/goravel/framework/contracts/database/driver"
	"github.com/goravel/framework/database/db"
	ormbuild "github.com/goravel/framework/database/orm"
	schemabuild "github.com/goravel/framework/database/schema"
)

// assemble builds the DB, Schema and close function for one connection, the way
// the framework does in production. Both engines go through here so the three
// pieces cannot drift apart, and a test connection is the program's connection
// rather than a facade's.
func assemble(t testing.TB, cfg Config, name string, driver contractsdriver.Driver, logger *Log) (*Conn, error) {
	t.Helper()
	ctx := t.Context()
	handle, err := db.BuildDB(ctx, cfg, logger, name, nil)
	if err != nil {
		return nil, err
	}
	orm, err := ormbuild.BuildOrm(ctx, cfg, name, logger, nil, nil)
	if err != nil {
		return nil, err
	}
	sch, err := schemabuild.NewSchema(cfg, logger, orm, driver, nil)
	if err != nil {
		return nil, err
	}
	c := &Conn{DB: handle, Schema: sch, Log: logger, Name: name}
	c.closeDB = func() error {
		pool, err := orm.DB()
		if err != nil {
			return err
		}
		return pool.Close()
	}
	return c, nil
}
