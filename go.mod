// SPDX-License-Identifier: Apache-2.0

module github.com/FreshLabDev/quoto

go 1.26.10

require github.com/jackc/pgx/v5 v5.10.0

require (
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/text v0.43.0 // indirect
)

// Until tg is released, the sibling worktree with the protocol additions Quoto
// needs. Replace this line with a tag before review: CI has no ../tg-quoto.
replace github.com/FreshLabDev/tg => ../tg-quoto
