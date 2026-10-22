// SPDX-License-Identifier: Apache-2.0

package db

import "github.com/FreshLabDev/quoto/internal/domain"

// Store is the one implementation of every shared port.
var (
	_ domain.Core      = (*Store)(nil)
	_ domain.Groups    = (*Store)(nil)
	_ domain.Messages  = (*Store)(nil)
	_ domain.Reactions = (*Store)(nil)
	_ domain.Quotes    = (*Store)(nil)
)

var _ domain.StatsReader = (*Store)(nil)
