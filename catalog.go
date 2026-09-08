// Package toolcatalog embeds Pomeforge's reviewed tool lock once and exposes a
// validated bootstrap catalog to the CLI and core integration.
package toolcatalog

import (
	"bytes"
	_ "embed"

	"pomeforge.local/pomeforge/internal/bootstrap"
)

//go:embed toolchains.lock.json
var lockedTools []byte

// Load returns a newly decoded and fully validated copy of the embedded lock.
func Load() (bootstrap.Catalog, error) {
	return bootstrap.Load(bytes.NewReader(lockedTools))
}
