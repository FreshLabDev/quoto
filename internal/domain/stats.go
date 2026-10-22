// SPDX-License-Identifier: Apache-2.0

package domain

import (
	"context"
	"time"
)

// Statistics count published quotes only.

// StatsQuote is the best quote of a group or of a person.
type StatsQuote struct {
	Text   string
	Score  float64 // 0..1
	Author string  // empty for a person's own stats, and when core does not know the name
	Date   time.Time
}

// AuthorWins is one line of a group's top authors.
type AuthorWins struct {
	Name     string // empty when core knows no name
	Wins     int
	AvgScore float64
}

// ChatStats is a group's statistics. TotalQuotes 0 means nothing else is set.
type ChatStats struct {
	GroupName     string // empty when core knows no title
	TotalQuotes   int
	UniqueAuthors int
	AvgScore      float64
	TopAuthors    []AuthorWins // up to three, most wins first
	Best          *StatsQuote
}

// UserStats is one person's statistics in one group. Wins 0 means nothing else
// is set.
type UserStats struct {
	UserName          string
	Wins              int
	AvgScore          float64
	Rank              int // 1 + the number of authors with more wins
	TotalParticipants int
	Best              *StatsQuote
}

// StatsReader reads statistics.
type StatsReader interface {
	// Chat returns nil when the group has no settings row.
	Chat(ctx context.Context, chatID int64) (*ChatStats, error)
	// User returns nil when the group has no settings row.
	User(ctx context.Context, chatID, userID int64) (*UserStats, error)
}
