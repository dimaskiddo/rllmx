// Package commands holds the Artisan commands. Each manages one thing on the
// local store; the database ones share a runtime that opens the store, holds the
// data lock and asks for confirmation the same way.
package commands
