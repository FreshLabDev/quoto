// SPDX-License-Identifier: Apache-2.0

// Package migrations embeds the schema migrations into the binary. Down
// migrations (migrations/down) are for CI only and are not embedded.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
