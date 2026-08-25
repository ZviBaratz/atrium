package theme

// tokens.go is the one place the eighteen semantic palette tokens are NAMED, and the one
// place that number is written down in this package. Everywhere else says "a palette
// token" or points here: readme_theme_test.go binds the README's spelling of it to
// len(TokenNames()), and nothing binds a Go comment, so a restated count is a claim that
// rots the moment the palette grows.
//
// Three consumers need the same list and cannot be allowed to disagree about it:
// the contrast oracle (contrast.go) keys its floors by these names, a user theme
// file (ui/theme/themefile) keys its palette overrides by them, and the README
// documents them. Before this table each of those would have spelled its own copy,
// and a nineteenth token would have been added to the struct with nothing noticing
// that no file could set it.
//
// The names are snake_case rather than the Go field spelling because they are the
// ON-DISK vocabulary — the same choice config.json makes for every other key a user
// types.
//
// The accessor returns a POINTER into the palette, so one table serves both
// directions: the oracle reads through it, SetToken writes through it. A getter-only
// table would have needed a second, hand-maintained setter table, which is precisely
// the drift this file exists to remove.
var paletteTokens = []struct {
	name string
	at   func(*Palette) *Color
}{
	{"bg", func(p *Palette) *Color { return &p.Bg }},
	{"bg_elevated", func(p *Palette) *Color { return &p.BgElevated }},
	{"bar_bg", func(p *Palette) *Color { return &p.BarBg }},
	{"fg", func(p *Palette) *Color { return &p.Fg }},
	{"fg_dim", func(p *Palette) *Color { return &p.FgDim }},
	{"fg_faint", func(p *Palette) *Color { return &p.FgFaint }},
	{"accent", func(p *Palette) *Color { return &p.Accent }},
	{"accent_muted", func(p *Palette) *Color { return &p.AccentMuted }},
	{"purple", func(p *Palette) *Color { return &p.Purple }},
	{"success", func(p *Palette) *Color { return &p.Success }},
	{"success_dim", func(p *Palette) *Color { return &p.SuccessDim }},
	{"working", func(p *Palette) *Color { return &p.Working }},
	{"pending", func(p *Palette) *Color { return &p.Pending }},
	{"attention", func(p *Palette) *Color { return &p.Attention }},
	{"danger", func(p *Palette) *Color { return &p.Danger }},
	{"cyan", func(p *Palette) *Color { return &p.Cyan }},
	{"badge_bg", func(p *Palette) *Color { return &p.BadgeBg }},
	{"badge_fg", func(p *Palette) *Color { return &p.BadgeFg }},
}

// TokenNames returns the on-disk names of the semantic palette tokens, in the order
// they are declared on Palette. A caller may not mutate the result.
//
// Ordered rather than sorted because the declaration order groups the tokens by role
// — backgrounds, then text, then accents, then status — which is the order a person
// authoring a palette wants to read them in. Nothing depends on it being sorted.
func TokenNames() []string {
	names := make([]string, 0, len(paletteTokens))
	for _, t := range paletteTokens {
		names = append(names, t.name)
	}
	return names
}

// SetToken sets one palette token by its on-disk name, reporting whether the name is
// one. An unknown name changes nothing, which is what lets a loader refuse it by name
// rather than absorbing a typo silently.
func SetToken(p *Palette, name string, c Color) bool {
	if p == nil {
		return false
	}
	for _, t := range paletteTokens {
		if t.name == name {
			*t.at(p) = c
			return true
		}
	}
	return false
}

// tokenAt returns the accessor for a token name, or nil.
//
// The oracle does NOT use it — Validate walks paletteTokens directly, since it wants
// every token in declaration order rather than one by name. Its caller is
// contrast_test.go's light/dark twin sweep, which walks TokenNames, keeps the floored
// ones, and needs the field each names. Kept unexported for that reason: it is a lookup
// into this file's table, not part of the palette vocabulary anyone outside it consumes.
//
// So both it and TokenNames project paletteTokens, and a "tokenAt knows every name
// TokenNames returns" assertion cannot fail. What is worth pinning is the pair of
// reflection guards below, which hold that table against the Palette struct.
func tokenAt(name string) func(*Palette) *Color {
	for _, t := range paletteTokens {
		if t.name == name {
			return t.at
		}
	}
	return nil
}
