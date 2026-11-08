// Package tests holds helpers shared by the test files of several packages.
//
// This root package may import only packages whose own tests do not import it
// (today app/file, app/dialect, app/document and app/sealer), because a test
// file that imports tests would otherwise close an import cycle. A helper that
// needs a package whose tests do use tests lives in a sub-package instead:
// tests/store builds a repository.Store, tests/usage records usage rows.
package tests
