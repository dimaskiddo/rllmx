package cache

import (
	gocache "github.com/goravel/framework/contracts/cache"
)

// noStores is a facade whose stores cannot be built: Store returns nil, as the
// real one does when a driver fails to start.
type noStores struct{ gocache.Cache }

func (noStores) Store(string) gocache.Driver { return nil }
