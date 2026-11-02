// Package starter holds the shared layer fleet init lays down in a new fleet
// config: agent instructions that point at the node guide, settings that
// record which agent acted and pre-approve only fleet's inert commands, and
// the skill that teaches agents to operate the fleet.
package starter

import "embed"

// Home is the starter shared layer, rooted at "home".
//
//go:embed home
var Home embed.FS
