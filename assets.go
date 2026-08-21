// Package wsm embeds the web assets and the OpenAPI spec so the compiled
// binary is fully self-contained.
package wsm

import "embed"

//go:embed web/templates web/static openapi.yaml
var FS embed.FS
