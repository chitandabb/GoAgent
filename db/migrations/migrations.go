// Package migrations exposes the versioned SQL files embedded in every Go role.
package migrations

import "embed"

// Files keeps migrations available regardless of the process working directory.
//
//go:embed *.sql
var Files embed.FS
