package providers

import (
	"errors"

	"github.com/goravel/framework/contracts/binding"
	"github.com/goravel/framework/contracts/foundation"

	"rllmx/app/platform/cache"
	"rllmx/app/platform/settings"
)

// BindingCache is the container key of the grouped cache the request path reads.
const BindingCache = "rllmx.cache"

// CacheOf returns the bound cache. A build that failed panics with the
// provider's own error, which the start prints as one line; a missing binding
// means the provider is not registered, a bug in the program, and panics too.
func CacheOf(app foundation.Application) *cache.Cache {
	v, err := app.Make(BindingCache)
	if err != nil {
		panic(err)
	}
	c, ok := v.(*cache.Cache)
	if !ok || c == nil {
		panic("providers: " + BindingCache + " is not bound")
	}
	return c
}

// CacheServiceProvider owns the grouped cache. Register binds it over the
// framework's Cache facade; nothing reads it before 1.8, but binding it here is
// what makes the REDIS_* settings decide a store instead of being checked and
// discarded at boot.
type CacheServiceProvider struct{}

// Relationship names what the provider binds and what it reads.
func (r *CacheServiceProvider) Relationship() binding.Relationship {
	return binding.Relationship{
		Bindings:     []string{BindingCache},
		Dependencies: []string{binding.Config, binding.Log},
	}
}

// Register binds the cache. With no Redis host the shared groups stay in the
// process memory store, so no request fails because a store was never built.
func (r *CacheServiceProvider) Register(app foundation.Application) {
	app.Singleton(BindingCache, func(app foundation.Application) (any, error) {
		cfg := app.MakeConfig()
		if cfg == nil {
			return nil, errors.New("providers: the config facade is not set")
		}
		log := app.MakeLog()
		s, err := settings.LoadCache(cfg)
		if err != nil {
			return nil, err
		}
		opts := []cache.Option{cache.WithLog(log)}
		if !s.Shared {
			opts = append(opts, cache.WithoutShared())
		}
		return cache.New(app.MakeCache(), opts...), nil
	})
}

// Boot has nothing to start.
func (r *CacheServiceProvider) Boot(app foundation.Application) {}
