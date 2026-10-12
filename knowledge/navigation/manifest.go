// Package navdata embeds the generated navigation manifest so the binary is
// self-contained: no file path to resolve at startup, no knowledge/ tree to
// ship alongside it, and no runtime failure mode for data that is fixed at
// build time.
//
// The package lives in this directory only because go:embed cannot reach above
// its own package. pages.yaml stays exactly where the generator writes it, so
// the §12.2 layout and `git diff --exit-code knowledge/` drift detection are
// both unaffected. See README.md for how the file is regenerated.
package navdata

import _ "embed"

//go:embed pages.yaml
var PagesYAML []byte
