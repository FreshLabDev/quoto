// SPDX-License-Identifier: Apache-2.0

// Package buildinfo holds what identifies this build and this project. The About
// card, /healthz and the OpenRouter attribution headers read it from here, so a
// value is written once.
package buildinfo

// Set by -ldflags "-X github.com/FreshLabDev/quoto/internal/buildinfo.Version=..."
// in the Dockerfile; "dev" marks a build made by hand.
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

const (
	Name       = "Quoto"
	License    = "Apache-2.0"
	Operator   = "Asterfield"
	Repository = "https://github.com/FreshLabDev/quoto"
	// Contact is the Telegram handle of the operator, shown in About and in the
	// user agreement.
	Contact    = "@amtiyo"
	ContactURL = "https://t.me/amtiyo"
)
