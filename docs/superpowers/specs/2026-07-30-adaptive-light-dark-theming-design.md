# Adaptive light/dark theming + `NO_COLOR` (#394)

Status: approved 2026-07-30. Six stages, A–F.

Stage labels are letters on purpose. #393's programme numbered its PRs, and "PR 6"
already means its merged v2-natives commit (`c57aa852`) — reusing numbers here
would collide with a closed programme.

## Problem

All three registered themes are dark-tuned (`ui/theme/registry.go`), so on a
light-background terminal Atrium's chrome ranges from low-contrast to
unreadable. `NO_COLOR` is honoured nowhere. Detection has to come first, because
a light palette nobody can select automatically is a palette nobody uses.

## What the tree actually says

Read before designing; each of these changed a decision.

**Atrium never paints a full-screen background.** `Palette.Bg` has exactly two
non-fade consumers, and both use it as a *foreground* on a filled chip
(`ui/overlay/styles.go:28`, `app/banner.go:59`). The canvas is the terminal's own
background. So `Bg` semantically means "the colour of the void", a light palette
is mostly a foreground retune, and the two `Foreground(Bg)` sites do the right
thing for free — provided the light `Accent` and `Attention` are saturated enough
to carry near-white text.

**Two hardcoded colours exist in the entire app**, both deliberate:
`ui/theme/agent.go`, `#d97757` (Claude) and `#4285f4` (Gemini), documented
as brand colours and therefore theme-independent. Measured contrast against
tokyo-night's `Bg`: 5.47 and 4.80. The contrast oracle must cover them, or it
exempts the only two colours the palette does not own.

**Falsified by Stage C, and left corrected here rather than as-written:** this
paragraph used to say both survive a light background as glyphs, so they stay.
They do not. `#d97757` peaks at **3.12:1 against pure white**, so it cannot clear
the 3.0 brand floor on any real light background — it measures 2.41 on
tokyo-night-day and 2.76 on catppuccin-latte, and no palette tuning reaches it,
because the colour is not the palette's to tune. Stage C shipped a second table,
`agentColorsLight`, holding the same two hues darkened only as far as the floor
requires, selected by polarity inside `AgentGlyph`. The general rule that falls
out: **compute a colour's ceiling against pure white before assuming it has a
legible form on paper** — a hue with a high L\* does not, and that is a property
of the brand, not of the palette.

**Palette tokens have few direct consumers.** 152 `theme.Current()` call sites,
but most route through the `*Style()` helpers; direct `Palette.X` reads run 1–26
per token. A light palette is a pure data addition with **zero** code change at
those sites.

**Nothing validates the configured theme name** — `theme.Get` falls back to the
default for unknown names, and no config path rejects one. `theme: auto` is
therefore already inert-safe on disk today, so introducing it needs no migration.

## Corrections to the issue

### 1. Mode 2031 is decodable in the pinned stack — and still deferred

The issue says 2031 "isn't yet a typed v2 message." It is reachable:

- `x/ansi@v0.11.7` carries `LightDarkMode`/`SetLightDarkMode`/
  `RequestLightDarkReport`/`LightDarkReport`.
- ultraviolet decodes `CSI ? 997 ; N n` into `uv.DarkColorSchemeEvent` /
  `uv.LightColorSchemeEvent` (`decoder.go:432`).
- `translateInputEvent` ends in `return e` (`input.go:56`), so unrecognised
  ultraviolet events reach `Update` verbatim. `tea.Msg = uv.Event`, so a
  `case uv.LightColorSchemeEvent:` compiles today.
- `tea.RawMsg` (`raw.go:5`, handled `tea.go:858`) can emit the DECSET.

**Deferred anyway, for a reason that does not expire.** 2031 is a *persistent
mode*, and Bubble Tea unwinds only the modes it owns declaratively through
`tea.View`; `restoreTerminalState` (`tty.go:33`) restores termios and nothing
else. An unmatched `\x1b[?2031h` therefore leaks at three points:

1. past quit;
2. past the **signal** path — `app.Run` returns on `ctx.Done()` without
   dispatching `Update`, so no Cmd-based reset can run, and the reset would have
   to be a direct tty write beside `reconcileInFlightStarts`;
3. past every `tea.Exec` attach — where the terminal keeps emitting
   `CSI?997;Nn` into an input stream **tmux now owns**, injecting stray bytes
   into the agent pane.

OSC 11 is a query: stateless, nothing to unwind. The issue reached the same
conclusion from "not typed yet", which would have expired on the next dependency
bump. This reason will not. Follow-up: pair 2031 with #396 (kitty keyboard),
which has the identical set-and-unwind problem and must build that machinery
anyway.

### 2. `NO_COLOR` is not free

Bubble Tea already runs `colorprofile.Detect(p.output, p.environ)`
(`tea.go:1083`), and colorprofile does consult `NO_COLOR` — through
`strconv.ParseBool`. Measured:

| `NO_COLOR=` | `1` | `true` | `0` | `false` | `yes` | `x` | `2` | absent |
|---|---|---|---|---|---|---|---|---|
| profile | Ascii | Ascii | TrueColor | TrueColor | TrueColor | TrueColor | TrueColor | TrueColor |

no-color.org mandates colour off when the variable is **present and non-empty,
regardless of value**. `NO_COLOR=yes` is silently ignored. Atrium needs its own
check. (`CLICOLOR_FORCE=1` correctly does *not* override `NO_COLOR=1`.)

### 3. `colorprofile.Ascii` is the right profile; `NoTTY` is a trap

Measured through `colorprofile.Writer`:

```
Ascii   "ESC[1mbold+colourESC[m plain ESC[3mitalic-256ESC[m ESC[mansi-redESC[m ESC[4munderlineESC[m"
NoTTY   "bold+colour plain italic-256 ansi-red underline"
```

`Ascii` drops every colour form — including the overlay fade's hand-written
`\x1b[31m` — and keeps bold/italic/underline. `NoTTY` flattens the hierarchy
that makes a monochrome UI navigable at all.

### 4. The splash's luminance ramp is fresco's, and it is directional toward black

The issue states "fresco itself needs no change beyond Atrium supplying
light-tuned tokens." That is false for the luminance channel.
`shade.go`'s `splashLumHexAt` walks L\* from `rainRampFloor` (near-black) *up* to
the hue, and `shadeAt`'s own comment says it: *"stop 0 is near-black ink on a
dark pane and never worth emitting."* On a light field the dim cells become the
**most** visible ones — the vignette edge inverts into a dark halo. Atrium
supplies hues (A0–A3 + Highlight); the ramp's direction and floor are fresco's,
and `rainRampFloor` is not a parameter.

The escape hatch is one Atrium already wires: `fresco.Options.LumRange`. At
`lumRange 0`, `shadeAt` short-circuits (`return hue, true`), the ramp is never
consulted, and all brightness rides glyph density — a documented,
byte-identity-guaranteed endpoint. One knob covers light mode *and* `NO_COLOR`
(where colour is gone, so a colour-borne brightness channel carries nothing).

Residual risk is aesthetic, not structural: fresco records lumRange 0 as "a
scatter of dots is confetti rather than dimming" on dark. Whether it reads on
light is a screenshot question, decided in Stage C.

### 5. AC#6 is misframed

Atrium's TUI runs **in the real terminal, not inside its tmux server** —
`app/app.go:684` states this, and it is why focus reporting reaches Atrium
directly. tmux 3.6's 2031 forwarding therefore concerns the *agents in the
panes*, not Atrium's theming. The real constraint is that detection is blind
during an attach.

## Hazards

### H1 — the tmux bar gap (a bug that exists today)

`ComposeSessionContext` pushes `#[fg=…]` markup per metadata tick and
`ArmContext` fires whenever the composed string changes, so the bar's **text**
colours already follow a live theme change. Only `status-style` — baked into the
managed conf at `session/tmux/config.go:79-80`, read by tmux only when the
*server* starts — is frozen. **The bar is already half-diverged on any live theme
change today**, which is a pre-existing defect independent of light mode.

`status-style` is set with `-g` (server-global), so the fix is one subprocess for
the whole fleet, mirroring `PushContext` exactly:

```
tmux -L <socket> set-option -g status-style "bg=…,fg=…" ; refresh-client -S
```

Regenerate the on-disk conf as well, so a server that starts *later* matches.
Both paths covered means no divergence and no need to gate the auto-flip.

### H2 — the fade replaces, it does not blend

`PlaceOverlay` rewrites every background SGR to `theme.Bg` and every foreground
SGR to `FgFaint`. No arithmetic can invert. Two real constraints instead:

- A light palette needs `FgFaint`/`Bg` contrast that is low but nonzero **in the
  dark-on-light direction**. Pinned by the contrast oracle (Stage B).
- `simpleColorRegex` requires at least one digit, so lipgloss v2's implicit reset
  `\x1b[m` passes through unrewritten. Faded regions revert to the **terminal's**
  background mid-line — invisible when `theme.Bg` ≈ the terminal's, visible when
  they diverge. A light-specific artifact to eyeball, not to code around.

### H3 — detection is blind during an attach

`tea.Exec` suspends the loop and tmux owns the terminal, so no OSC 11 reply and
no focus event reach Atrium. `repaintAfterAttach` (`app/app_layout.go:429`)
already batches a synthetic `WindowSizeMsg` after every detach; the
background-colour request joins that batch. One argument, at the one moment we
know detection was blind.

### H4 — the splash

See correction 4. In scope via `LumRange: 0`; a proper fresco light ramp is a
follow-up in that repo, filed as ZviBaratz/fresco#82.

**Resolved in Stage C.** The fallback this section used to name — splash-off on a
light palette — was rejected on the evidence. `LumRange: 0` reads on light for
four of the five variants, measurably better than the ramp does (a density
vignette survives the polarity flip; a luminance one does not). Only **rain**
fails, and it fails hard: its brightness is entirely luminance, so at 0 the pane
fills solid — 95% of cells inked, edge:core 83:100, no vignette at all. Rain is
therefore exempt from the rung and stays on the ramp, which is merely inverted.
Turning the splash off would have discarded four working variants to avoid one
broken one.

### H5 — mode 2031 leaks (new)

See correction 1. Resolved by deferring 2031.

## Design

### `auto` is a third axis, not a fourth registry entry

`theme.Get` must return a concrete 18-token `Palette`, so an `auto` registry
entry would hold a fiction that `TestSplashPalettesAreCanonicalHex` and the new
contrast oracle would then dutifully test. Meanwhile `ui/theme/current.go`
already composes the active theme from two orthogonal axes (`curName`,
`curGlyphSet`); scheme is a natural third.

- Light twins register as **ordinary named themes** — `tokyo-night-day`,
  `catppuccin-latte`. They are independently selectable, appear in the settings
  picker for free, and inherit every existing palette guard. *(Shipped: the
  `options` closure returns `theme.SelectableNames()`, not `theme.Names()` — see
  the `auto` note below.)*
- A `dark → light` pair link, plus `theme.SetScheme(dark bool)` alongside `Set`
  and `SetGlyphSet`, returning a `restore func()` like its siblings.
- `auto` is a **reserved name** resolved in `compose()`, never a registry entry.
  It means "the default family's pair, per detection". *(Shipped differently:
  `Names()` deliberately still returns only the registry, and
  `SelectableNames()` — `auto` first, then the sorted registry — is what the
  picker reads. Putting `auto` in `Names()` would have made every
  registry-wide palette guard iterate a name with no palette, passing
  vacuously.)* Note in the test that
  `TestSplashPalettesAreCanonicalHex` passes *vacuously* for `auto` (because
  `Get("auto")` yields the default) and is not coverage of it.
- **AC#4 becomes structural**: only the literal `auto` consults the scheme axis,
  so an explicitly named theme cannot auto-switch.

**Only the default family needs a twin.** A `catppuccin-mocha` user wanting
adaptivity selects `auto` and gets tokyo-night. Per-family adaptivity would need
a `theme_mode` field — 4 drift sites, a second settings row, and an
invalid-combination space (`catppuccin-mocha` + `light` with no twin). Rejected
as YAGNI until someone asks. `unicode` needs no twin; it is a back-compat entry
that reuses tokyo-night's palette with square borders.

### `NO_COLOR` is a render-time profile override

Not a theme, not a palette transform. It is the only route that also covers the
non-lipgloss emitters *inside* the frame (measured: the fade's raw SGR is
converted), it preserves bold/italic/underline that a mono palette cannot
restore, and it adds no 19th palette to keep in step with 18 tokens.

Two surfaces the profile cannot reach, handled explicitly:

- **tmux markup** — `#[fg=…]` in `ComposeSessionContext` and `status-style` in
  the managed conf. tmux renders these; the profile never sees them. This is the
  most colour-saturated surface Atrium owns, so skipping it would violate
  `NO_COLOR` inside every attached pane. Omit the `#[fg=]` wrappers, keep
  `#[bold]`, emit no `status-style` colours.
- **the splash's brightness channel** — `LumRange: 0`, else the field loses its
  structure the moment colour is stripped.

### Detection ladder

A pure resolver, no I/O:

```
scheme(bg *color.Color, colorfgbg string) → dark | light | unknown
```

Rungs, highest first: OSC 11 reply (`tea.BackgroundColorMsg.IsDark()`) →
`COLORFGBG` → unknown. Two rules make it safe:

- **Latching.** No reply means "keep the current scheme", never "flip to the
  default on timeout". Absence of evidence is not evidence.
- **`COLORFGBG` never corrects a later OSC 11 answer.** It is stale-prone — it
  survives into child processes after a terminal theme change — so it is a rung
  *below* OSC 11 and consulted only when OSC 11 was silent.

Query points: `Init` (startup), `tea.FocusMsg` (refocus), and
`repaintAfterAttach` (detach). *(Shipped with a fourth, found in review: the
settings panel's theme arm, via `applySchemeQueryCmd`. The three here all ask on
behalf of a selection that was already `auto`; selecting `auto` is the one site
where the gate that suppressed every earlier query is itself what changed.)*
`app/app_update.go:386` currently sets
`m.focused = true` and returns nil; adding a Cmd is purely additive and does not
touch `m.focused`, so there is no interference with the notification gating in
`app_notify`. The query itself is a ~5-byte write.

### Live re-theme

`applySettingChange("theme")` (`app/app_layout.go:266`) already re-`Set`s the
theme, re-seeds the spinner frames, and forces a repaint; styles read
`theme.Current()` lazily. A detection-driven flip routes through the same path,
plus H1's bar push.

## Verification

The #393 oracle separation is the backbone: plain-text frames are the layout
oracle, colour is a separate fingerprint that moves only deliberately.

| Oracle | What it proves | Moves in |
|---|---|---|
| `app/testdata/frames/*.txt` (18 states × 2 sizes) | a theme change is a colour change, not a layout change | **never** |
| `app/testdata/colours.txt` | the default theme's rendered colour is unchanged | **never** — see below |
| `app/testdata/colours-light.txt` (new, Stage C) | the light palette renders as intended, all 18 states | C only |
| contrast oracle (new, Stage B) | the light palette is as readable as its dark twin | B, C |
| `NO_COLOR` writer oracle (new, Stage D) | no colour survives; bold/italic/underline do | D |

`colours.txt` never moves in this programme, and that is the point rather than an
oversight: it is generated at the default theme, Stages A–D do not change what the
default renders, and Stage E/F's `auto`-with-no-detection resolves to that same
default. A move means something leaked. The light palette gets its own
fingerprint — the same 18 states rendered under `tokyo-night-day` — so it is
guarded by construction instead of by the absence of a guard.

Two subtleties that decide where guards go:

- **The colour fingerprint reads `View().Content`, which is pre-writer.** The
  `NO_COLOR` profile route is therefore *invisible* to it. Hence a separate
  oracle: pipe each state's frame through `colorprofile.Writer{Profile: Ascii}`,
  assert zero colour SGR **and** that bold/italic/underline survive. The second
  half is what stops a later "fix" using `NoTTY`.
- **`newParityHome` pins the configured theme** (`cfg.GetTheme()` since Task 7,
  `app/frameparity_test.go:171`). Once `auto` is the default (Stage F) that
  fixture depends on the scheme axis, so it must pin `SetScheme(dark)` explicitly
  or the goldens become detection-sensitive under `-shuffle`. *(Shipped in Stage
  E rather than Stage F: the axis is a package global other tests mutate, so the
  pin was needed as soon as the axis existed, not as late as the default flip.)*

### The contrast oracle

Nothing in the suite today can see "unreadable". A WCAG-ratio test over every
registered theme, plus the two `agent.go` brand colours, asserting:

- each light theme's per-token contrast is **within tolerance of its dark twin's**
  — a *relative* bar, so there are no taste constants to argue about and it
  directly encodes "as readable as the dark one";
- an absolute floor for the tokens that carry text.

It already found a live defect while being designed. `barState`
(`ui/contextbar.go:31`) renders Paused and the default state with `FgDim` on the
`BarBg` band: **1.44:1** on tokyo-night, **1.87:1** on catppuccin-mocha — while
`ui/contextbar.go:59`'s own comment says "dim greys wash out" there.
**Report it, exempt that one pair with a reason, do not fix it in this issue.**

`FgFaint == BarBg` in *both* themes (`#414868`, `#45475a`) is **not** a defect —
it is a shared deliberate choice, the faint rule and the bar band being the same
slate. Both floors below are set against it rather than against an assumption
that faint text must be legible.

Reference ratios the light twin must reproduce, measured on both shipped dark
themes (tokyo-night / catppuccin-mocha, vs each one's own `Bg`):

```
Fg          10.59 / 11.34     Pending/Cyan   9.96 / 10.54
Success      9.35 / 11.03     Attention      8.55 / 12.91
Purple       7.39 /  8.07     Accent         6.79 /  7.79
Danger       6.46 /  7.08     SuccessDim     4.35 /  4.60
FgDim/Working 2.76 / 3.36     AccentMuted    2.56 /  8.69
FgFaint/BarBg 1.91 / 1.80     BgElevated     1.17 /  1.30
BadgeFg on BadgeBg  7.39 / 8.07    Bg on Accent      6.79 / 7.79
Bg on Attention     8.55 /12.91    Fg on BgElevated  9.02 / 8.69
Fg on BarBg         5.53 / 6.31
agent claude #d97757  5.47 / 5.25   agent gemini #4285f4  4.80 / 4.60
```

Floors are set from the **minimum across both themes** with margin, so the oracle
lands green on what ships today: 4.5 for the text-and-status tokens, 3.0 for
`SuccessDim`, 2.4 for `FgDim`/`Working`/`AccentMuted`, 1.6 for `FgFaint` and
`BarBg`, 1.1 for `BgElevated`, and 4.5 for each pair above. `AccentMuted`'s
2.56-vs-8.69 spread is why it gets its own low floor rather than sharing
`Accent`'s.

### Gates

`PATH=$PATH:$HOME/go/bin just ci` (golangci-lint is at `~/go/bin`, off mise's
PATH), plus `go test -race -shuffle=on ./...`. Mutation-verify both new oracles.
Baseline confirmed green before planning.

### Live matrix (the part no Go test can do)

A light theme is the one change a passing suite cannot evaluate. Drive a real
light terminal **and** a real dark one, each including an attach → detach cycle:

- chrome legibility at 80×24 and at a roomy size;
- the overlay fade with a modal up (H2's mid-line reset artifact);
- the splash at `LumRange: 0` — the confetti question;
- the in-pane tmux status bar during an attach, and after a live theme flip;
- `NO_COLOR=1` and `NO_COLOR=yes` (the spec-compliance case);
- which detection rung answered, per terminal.

Isolation: `HOME` alone does not isolate the tmux socket — export `TMUX_TMPDIR`
too, under a short `/tmp` root (`sun_path` limit).

## Stages

Each lands green and independently. **A–D deliver real value with no detection
code at all**: a light-terminal user can select a readable theme after C, and a
`NO_COLOR` user is served after D.

### Stage A — the tmux bar follows a live theme change

Pre-existing bugfix, no light-mode content. A `session/tmux` helper that pushes
`status-style` to the live server (one batched subprocess + `refresh-client -S`,
off the update thread per #380), called from `applySettingChange("theme")`, plus
regenerating the managed conf.

Guards: a fake-executor test asserting one batched subprocess carrying the new
bg/fg; a test that `renderManagedConfig` reflects the *current* theme (mutation:
swap themes, assert the bytes move). Drift sites: none new.

### Stage B — the contrast oracle

Test-only, zero behaviour change. Must exist before any palette, because it is
the gate the palette must pass. Expected to surface the `FgDim`-on-`BarBg`
finding; exempt it with a reason and file it separately.

### Stage C — the light palettes

`tokyo-night-day` and `catppuccin-latte` as ordinary named themes, sourced from
their canonical upstreams (folke/tokyonight.nvim `day`; catppuccin `latte`) and
**validated by the contrast oracle rather than by recall** — the oracle is what
makes the values right.

Data only: no `auto`, no detection, no code change at the 152 `theme.Current()`
sites. Immediately usable via `theme: tokyo-night-day`, which is what makes the
light eyeball round possible this early.

Guards for free: canonical-hex validation, contrast, glyph widths, settings
picker cycling (both existing `theme.Names()` consumers are length-agnostic; the
picker itself moved to `SelectableNames()`).
**The frame goldens and `colours.txt` must both stay byte-identical** — the
default still renders what it rendered, so if either moves, something leaked.
`colours-light.txt` is added here, baselined once with the diff read.

Splash: evaluate `LumRange: 0` on a real light terminal. **Done — it reads**, for
four variants of five; rain is exempt and stays on the ramp (see H4). The
splash-off-on-light fallback this paragraph used to hold in reserve was rejected
rather than taken. The fresco light-ramp follow-up is filed as
ZviBaratz/fresco#82.

### Stage D — `NO_COLOR`

Spec-compliant check (present and non-empty) forcing `colorprofile.Ascii`; tmux
markup and `status-style` decolourised; splash `LumRange: 0` — **rain exempt
here too**, since what makes 0 fill its pane solid is the variant's own
luminance-only brightness, not what stripped the colour; the writer-based
oracle. Independent of theming.

### Stage E — detection, `theme: auto`, live re-theme

The scheme axis; the pure ladder resolver (table-tested, including the
unknown→dark default and `COLORFGBG` parsing); `tea.RequestBackgroundColor` in
`Init`; `tea.BackgroundColorMsg` in `Update`; focus re-query; detach re-query in
`repaintAfterAttach`; reuse of Stage A's bar push on every flip. `atrium doctor`
reports the detected scheme and which rung answered.

AC#5 proof: with `auto` and no detection input, `compose()` resolves to exactly
`Get(DefaultThemeName)`, and `colours.txt` stays **byte-identical**. That is a
zero-regression proof across the whole UI, not a spot check.

Drift sites touched: `README.md`'s `theme` row (the `auto` value and its
meaning); the settings row's `summary`; `config/types.go`'s `Theme` doc comment.
No new `Config` field, so the 4-site new-field set does not apply.

### Stage F — make `auto` the default

Gated on Stage E's live matrix: land A–E, drive the real terminals, then flip
`config.go:92` and the settings row's `defaultDisplay` in their own commit. If
Stage E is correct this is self-proving — `auto` with no detection resolves to
`tokyo-night`, so **not one golden byte moves**, and the goldens prove the
default change is inert on dark terminals. Add the explicit `SetScheme(dark)` pin
to `newParityHome`. If any terminal in the matrix misbehaves, the fix ships and
the default does not.

## Acceptance criteria: verdicts

Stated plainly rather than satisfied nominally — #393 found two ACs that were not
achievable, and saying so early was worth more than meeting them on paper.

| AC | Verdict |
|---|---|
| 1 — light palette renders, splash not washed out | **Partial.** Chrome: achievable. Splash: at risk (correction 4). **Pane content is out of reach** — Atrium replays the agent's own ANSI via `capture-pane -e`, so a claude pane's colours are the agent's and cannot be themed. That is a large fraction of the screen and the issue does not say so. |
| 2 — flips live on an OS switch (2031) and on refocus | **Restated.** 2031 *is* decodable (correction 1) but deferred for lifecycle reasons, so this ships as **on refocus and on detach**, not instantly. |
| 3 — `NO_COLOR=1` fully monochrome, navigable | **Achievable, not free** (correction 2), and requires the tmux surfaces separately. |
| 4 — a named theme never auto-switches | **Achievable, structurally.** |
| 5 — detection failure falls back to dark, zero regression | **Achievable and provable — as a statement about the code path.** Byte-identical `colours.txt` with no detection input proves it across the whole UI. What cannot be proved in Go is *which terminals* take that path; that is the live matrix, not a test. |
| 6 — works on tmux 3.6+ (2031 forwarded); doctor notes older tmux | **Misframed** (correction 5). Rewrite as: detection is blind during an attach and re-queries on detach; doctor reports the detected scheme and which rung answered. |

## Follow-ups filed, not done here

- fresco: a light luminance ramp (invert `splashLumHexAt`'s direction, or make
  `rainRampFloor` a parameter). Blocks a properly-shaded light splash.
- Atrium: mode 2031, paired with #396's mode lifecycle.
- Atrium: `barState`'s `FgDim` on the `BarBg` band — 1.44:1 on tokyo-night,
  1.87:1 on catppuccin-mocha — a legibility defect on the *dark* themes,
  surfaced by Stage B. (`FgFaint == BarBg` is deliberate, not a defect.)
- Atrium: per-family adaptivity (`theme_mode`), only if asked for.
