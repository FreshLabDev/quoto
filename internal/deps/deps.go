// SPDX-License-Identifier: Apache-2.0

//go:build tools

// Package deps keeps the module requirements of packages that are not written
// yet. It is deleted once real code imports them.
package deps

import (
	_ "github.com/FreshLabDev/tg"
	_ "golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
	_ "golang.org/x/sync/semaphore"
	_ "golang.org/x/sync/singleflight"
)
