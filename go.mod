// SPDX-License-Identifier: Apache-2.0
module searchy

go 1.26.6

require (
	github.com/FreshLabDev/tg v0.0.1-alpha.7
	github.com/hashicorp/golang-lru/v2 v2.0.7
	github.com/jackc/pgx/v5 v5.10.0
	golang.org/x/image v0.45.0
	golang.org/x/sync v0.22.0
)

require (
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.41.0 // indirect
)

// TEMPORARY: the media and inline-mode surface searchy needs landed in tg but
// is not tagged yet. Drop this the moment tg publishes the minor release.
replace github.com/FreshLabDev/tg => ../tg
