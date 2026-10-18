// Package cricketstream holds the files built into the single CricketStream app. It is
// the module root only because go:embed can't reach above its own directory; everything
// else imports it through internal/assets.
package cricketstream

import "embed"

// Files is every asset the app ships with. A file of the same name beside the
// executable overrides the built-in copy (see internal/assets).
//
//go:embed overlay.html control.html scoring.html scoreboard.template
//go:embed config.example.ini match_state.example.json
//go:embed fonts brand graphics
//go:embed LICENSE THIRD_PARTY_NOTICES.txt
var Files embed.FS
