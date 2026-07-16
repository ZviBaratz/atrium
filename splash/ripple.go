package splash

// Ripple is the roster's event variant: drops fall on a dark pool, each one
// flashing at impact and expanding into a ring that shifts hue as it ages and
// interferes with every other ring it crosses.
//
// It is the only field where things *happen*. Rain falls forever and the tunnel
// flies forever; both are steady states, as was every organic field before them. A drop
// has a birth, a life and a death, and the pool between drops is not a quiet
// version of the field but the exact absence of one.
//
// Three of its properties are structural rather than taste, and each is
// commented where it lives: the field is |sum| so that interference can cancel
// (a pool is lit by slope, and two waves that meet out of phase stay dark), the
// spawn lattice is sized so that exactly the 3x3 neighbouring cells and 2 epochs
// can reach any point (which is what keeps a pure point function affordable),
// and every packet has compact support so that "can reach" is an exact statement
// rather than a threshold on a tail.

import "math"

const (
	// rippleW is the wave packet's half-width in aspect units: a drop's
	// disturbance lives entirely within rippleW of its ring crest and is exactly
	// zero beyond it (see rippleDropWave).
	//
	// The floor under it is the grid, not taste. A row step moves dy by
	// cellAspect (2.0) while a column step moves dx by 1, so the vertical axis
	// samples this field at half the horizontal rate — the trap that made rain's
	// heads blink and cost the tunnel two rounds. Ripple cannot mip its way out
	// of it the way the tunnel does, because there is nothing to fade toward: the
	// packet's spatial frequency is the same at every radius and every age, so
	// the only defence is to draw the ring coarse enough to sample.
	//
	// The rendered structure is |cos| of the carrier, whose period is
	// rippleW/rippleCyc aspect units — here 6.67, i.e. 3.3 rows vertically and
	// 6.7 columns horizontally, which is the same distance on a 2:1 cell and is
	// exactly what cellAspect exists to make true.
	//
	// That leaves ~3.3 samples per band on the worse axis, and what settles
	// whether that is enough is the crest's own width rather than the ratio. The
	// row grid steps x = (d-crest)/rippleW by cellAspect/rippleW = 0.2, so the
	// worst alignment straddles the peak at x = ±0.1 and reads
	// (1-0.1^2)^2 * cos(0.15pi) = 87.3% of it — comfortably enough that a ring
	// cannot fall between two rows and blink, which is what rain's heads did.
	// Narrow the packet and that capture falls off (at rippleW 6 it is 67%); see
	// TestSplashRippleCrestSurvivesTheRowPitch, which pins it.
	rippleW = 10.0
	// rippleCyc is how many carrier half-cycles fit between the crest and the
	// packet's edge. The carrier is what makes this a wave rather than a bump,
	// and that part is categorical: strip it and the packet is a positive lump,
	// |sum| == sum, two rings can only ever brighten each other, and the
	// interference this variant is built around silently stops existing.
	//
	// The value itself is not categorical, and it is worth being exact about
	// that because the obvious story — "below some cycle count the trough falls
	// under the envelope's root and the packet goes positive" — is false.
	// Measured, the packet's minimum as a share of its crest runs 15% at cyc 1.0,
	// 29% at 1.25, 41% at 1.5 and 61% at 2: a smooth curve with no threshold
	// anywhere in it. So this is a choice about how hard rings cancel, and the
	// floor in TestSplashRipplePacketIsCompactAndSigned pins that choice rather
	// than a cliff.
	//
	// (The minimum does not sit where the carrier's own trough does. The envelope
	// is already falling there, so the product turns over earlier — at 1.5 it is
	// at x = 0.54, not at the carrier's 2/3.)
	//
	// What bounds it from above is the grid: the rendered band period is
	// rippleW/rippleCyc, so every extra ring per drop is bought with resolution
	// the vertical axis only has half of. See rippleW.
	rippleCyc = 1.5

	// rippleSpeed is how fast a ring's crest travels, in aspect units per phase
	// unit. phase advances driftPerFrame (0.015) per frame at ~60fps, i.e. 0.9
	// phase units a second, so 11 is ~9.9 units/second — about 5 rows or 10
	// columns a second, the unhurried spread of a drop on still water rather than
	// a shockwave.
	//
	// Per frame a crest moves 0.165 units against a band period of 6.67, so a
	// cell takes ~40 frames to cross one. That continuity is also what keeps the
	// frame-to-frame contract safe: a ring's radius is a continuous function of
	// phase, so consecutive frames differ everywhere a ring is passing rather
	// than only where some quantized counter happened to tick.
	//
	// Note that is a statement about a ring in flight, not about the field as a
	// whole: a drop's birth is a genuine discontinuity in time — its packet
	// appears at full flash amplitude in a single frame. That is the impact, and
	// it is the one place this field steps on purpose (see rippleFlashAmp).
	rippleSpeed = 11.0
	// rippleLife is how long a drop's ring lasts, in phase units — ~2.7 seconds
	// at 60fps. It has to be at most ripplePeriod; see rippleEpochs.
	//
	// With rippleW it also sets how much of a drop's life is a ring at all: the
	// packet only clears the origin once the crest outruns its own half-width, at
	// age rippleW/rippleSpeed — here 0.91, or 38% of the way through. Before that
	// the drop is a disc whose middle pulses as the wave leaves it, which is what
	// an impact looks like; after it, an expanding ring. Shortening the life (or
	// widening the packet) spends the ring phase on the disc.
	rippleLife = 2.4
	// ripplePeriod is how often a lattice cell spawns a drop, in phase units.
	// Together with the cell size it is the whole density knob: one drop per cell
	// per period, so a bigger pane covers more cells and therefore shows more
	// drops. That is the point — see splashFieldAt on why this field is drawn at
	// absolute scale while the tunnel is drawn relative to the pane.
	ripplePeriod = 2.4

	// rippleMaxR is the furthest a crest ever gets, and rippleCell is the spawn
	// lattice's pitch. Both are derived, not chosen, and rippleCell is the load-
	// bearing one: it is exactly the distance a drop's disturbance can reach, so
	// a drop two lattice cells away is provably unable to touch this point.
	//
	// The argument, which rippleReach spends: a point at dx lies in cell
	// i0 = floor(dx/rippleCell), and a drop in cell i is drawn *inside* its own
	// cell (see rippleDropPos), so for i >= i0+2 the drop's px >= i*cell while
	// dx < (i0+1)*cell, i.e. they are strictly more than one cell apart. One cell
	// is rippleMaxR + rippleW, and a packet centred at a crest no further out
	// than rippleMaxR is exactly zero past rippleW. So the contribution is not
	// small out there — it is zero, and the 3x3 window is exact rather than a
	// good approximation. That exactness is why the packet is a compact
	// polynomial and not a Gaussian or a rational: those have tails, and a tail
	// would turn this identity into a tolerance.
	rippleMaxR = rippleSpeed * rippleLife
	rippleCell = rippleMaxR + rippleW

	// rippleAmp is the amplitude a lone crest carries once its flash has passed,
	// and it is under 1 on purpose: the field clamps, so a crest at 1 would
	// render as a flat plateau with no shading in it, and — worse — two rings
	// meeting could not read as any brighter than one. The headroom is what makes
	// constructive interference visible.
	rippleAmp = 0.85
	// The impact flash: a short-lived boost at the moment a drop lands, and the
	// difference between rain on water and a screensaver of expanding circles.
	// It is allowed to blow past the clamp — an impact that saturates a few cells
	// is the one place in this field where a flat white core is the right answer.
	//
	// rippleFlashT is ~0.2 seconds, over which the crest travels 2 units: the
	// flash is an event at a place, not a thing that moves.
	rippleFlashAmp = 0.6
	rippleFlashT   = 0.18

	// rippleReach and rippleEpochs are how far the sum looks, in lattice cells
	// and in spawn epochs, and both are minimums proven above rather than budgets.
	//
	// rippleReach is 1 because rippleCell is the exact reach (see above), so the
	// 3x3 block around a point holds every drop that can touch it.
	//
	// rippleEpochs is 2 because rippleLife <= ripplePeriod. A drop of epoch e is
	// born inside [e*P, (e+1)*P) (see rippleDropBirth), and this point's phase sits
	// in epoch e0, so epoch e0 holds every drop already born and epoch e0-1
	// reaches back to (e0-1)*P <= phase - life — which is as far back as a drop
	// can still be alive. Epoch e0-2 is therefore always dead. Raising rippleLife
	// past ripplePeriod silently truncates the oldest rings instead of failing,
	// which is what TestSplashRippleLifeFitsTheEpochWindow exists to catch.
	//
	// Both are parameters of splashRippleSum rather than constants baked into it
	// so those proofs can be *run* rather than only read:
	// TestSplashRippleWindowIsExact sums a far wider window and requires bit-for-
	// bit the same answer, which makes this a claim about the geometry instead of
	// a claim about the argument above being read correctly.
	rippleReach  = 1
	rippleEpochs = 2
)

// The per-drop draws. Each is its own lattice field, so a drop's position and
// its birth time are independent rather than two views of one number.
const (
	seedRippleX uint32 = 0x2F6A5C13
	seedRippleY uint32 = 0x94C7E2B5
	seedRippleT uint32 = 0x5D3B1F87
)

// rippleEpochSeed folds a spawn epoch into a draw's seed, so a lattice cell's
// successive drops fall in unrelated places at unrelated times instead of
// re-staging the same drop forever.
//
// The epoch goes through lowbias32 before the xor rather than being xor'd raw:
// consecutive epochs differ in one or two low bits, and splashHash's innermost
// avalanche is over `seed ^ constant`, so a raw xor would hand it two nearly
// identical seeds and lean on that one pass to separate them. Mixing first
// makes consecutive epochs as unrelated as any two seeds.
//
// The int32 narrowing is defined here rather than merely convenient: e is
// floor(phase/ripplePeriod) and phase advances driftPerFrame a frame, so the
// epoch counter climbs at driftPerFrame/ripplePeriod = 1/160 per frame and needs
// ~3.4e11 frames — 180 years at 60fps — to reach int32's range. It can never be
// an Inf or a NaN to begin with.
func rippleEpochSeed(base uint32, e int) uint32 {
	return base ^ lowbias32(splashU32(int32(e))) //nolint:gosec // G115: see above — the epoch counter is centuries from this bound
}

// When and where lattice cell (i,j) spawns its epoch-e drop. They are two draws
// rather than one because *when* is what admits a drop and *where* is what only
// the survivors need — see rippleDropWave, which is the whole reason for the
// split.
//
// The two invariants here are what the 3x3x2 window rests on, and neither is
// obvious from the call site: the drop is born strictly inside its own epoch's
// window, and it lands strictly *inside* its own lattice cell. Both hold because
// splashCellHash returns [0,1) and both are pinned by
// TestRippleDropsStayInTheirCellAndEpoch — widen either jitter and the sum's
// window stops being exact, silently, on the cells nearest the boundary.
func rippleDropBirth(i, j, e int) (tStart float64) {
	return (float64(e) + splashCellHash(i, j, rippleEpochSeed(seedRippleT, e))) * ripplePeriod
}

func rippleDropPos(i, j, e int) (px, py float64) {
	px = (float64(i) + splashCellHash(i, j, rippleEpochSeed(seedRippleX, e))) * rippleCell
	py = (float64(j) + splashCellHash(i, j, rippleEpochSeed(seedRippleY, e))) * rippleCell
	return px, py
}

// rippleDropWave is one drop's signed contribution at a point, plus how far
// through its life that drop is. Signed is the operative word: this returns a
// displacement, and the caller sums displacements before taking a magnitude.
//
// ageT is meaningful only when c is non-zero, and that is a contract rather than
// an oversight: every zero return reports ageT 0, including the live drop whose
// packet simply does not reach this point. Nothing can read that as a lie,
// because ageT's only consumer weights it by |c| (see splashRippleSum), so a
// zero contribution carries zero weight whatever age rides along with it. The
// callers' `c == 0` skip is therefore an optimization, not a guard — but a
// future caller wanting "this drop's age" must draw it, not take it from here.
//
// The two early-outs are ordered by what they cost, which is why the birth draw
// and the position draw are separate (see rippleDropBirth). Being unborn or dead
// is decided by the birth draw alone, and it is the common case rather than an
// edge: rippleLife == ripplePeriod, so a lattice cell averages exactly one live
// drop across the two epochs the sum considers, and half the candidates are
// therefore decided before a position is worth drawing. Measured over 1.3M
// candidates at 240x60, 49.7% never get past this line — drawing the position
// first would hash two lattice fields for each of them and throw both away.
// (Of the rest, 43.7% are alive but out of packet and 6.6% contribute; those do
// need the position, which is why the gate sits here and not lower.)
//
// The shape is a compact wave packet — a raised-cosine carrier under a
// (1-x^2)^2 envelope, both in x = (d - crest)/rippleW. Three things follow from
// the envelope being polynomial with a root at x = +-1 rather than a Gaussian
// or a 1/(1+k^2) rational. Its support is exactly [-rippleW, rippleW], which is
// what lets rippleCell be an exact reach instead of an arbitrary cut. It meets
// zero with zero slope, so a drop entering or leaving a cell's 3x3 window fades
// in rather than clicking. And it is what gates the carrier: the cosine is only
// evaluated for a candidate the envelope has already admitted, so the packet's
// one transcendental is paid by the few drops that actually reach this cell
// rather than by all 18 it has to consider.
//
// The age fade is 1 - t^2, and the shape is a choice worth naming rather than a
// default. (1-t)^2 was the obvious form and is wrong here: it spends most of a
// drop's brightness during the phase when the packet still covers the origin —
// a filled disc, not a ring — so by the time the ring actually opens at t = 0.38
// (see rippleLife) it has already faded to a third. Measured over the same
// lives, 1 - t^2 holds 84% at t = 0.4 against (1-t)^2's 36%, which is what puts
// the brightness on the *expanding* ring. It still reaches exactly 0 at t = 1,
// so a drop dies out rather than popping — and there is no contrast window to
// hide a pop behind: Pass 2 runs one full-range curve with no floor under it.
//
// There is deliberately no separate spatial decay, though the design called for
// one. On the crest d == rippleSpeed*age, so a decay in d and a decay in age are
// the same function of the same quantity, reparametrized — the 1/sqrt(r)
// spreading of a real circular wave and the fade of a dying one are one knob
// here, not two. Applying both would only tilt the packet, brightening its inner
// flank against its outer one.
func rippleDropWave(i, j, e int, dx, dy, phase float64) (c, ageT float64) {
	age := phase - rippleDropBirth(i, j, e)
	if age < 0 || age > rippleLife {
		return 0, 0 // unborn, or dead — decided without drawing a position
	}
	px, py := rippleDropPos(i, j, e)
	ex, ey := dx-px, dy-py
	// Sqrt of the sum rather than math.Hypot: Hypot spends its cost scaling
	// against overflow, and these legs are pane-sized (a few hundred at most), so
	// there is nothing to protect against. One instruction instead of a call.
	d := math.Sqrt(ex*ex + ey*ey)

	x := (d - rippleSpeed*age) / rippleW
	s := 1 - x*x
	if s <= 0 {
		return 0, 0 // outside the packet, exactly — see rippleCell
	}
	t := age / rippleLife
	fade := 1 - t*t
	// Squared, so the flash reaches rippleFlashT with zero slope and settles into
	// the ring's own amplitude instead of stepping off a kink.
	f := 1 - clamp01(age/rippleFlashT)
	return rippleAmp * s * s * math.Cos(math.Pi*rippleCyc*x) * fade * (1 + rippleFlashAmp*f*f), t
}

// splashRippleAt evaluates the pool at one cell: how far the surface is
// displaced there, and which drop's colour that displacement is wearing.
//
// It is a pure point function with no per-frame state and no closure over a
// drop list, which is what the spawn lattice buys: instead of asking "where are
// the drops", a cell asks "which lattice cells could hold a drop that reaches
// me" and answers it with arithmetic. Nothing is allocated, nothing persists
// between frames, and the field is the same whether it is evaluated once or a
// million times out of order.
func splashRippleAt(_, _ int, dx, dy, phase float64) (val, aux float64) {
	return splashRippleSum(dx, dy, phase, rippleReach, rippleEpochs)
}

// splashRippleSum is splashRippleAt with the search window as parameters, so the
// window's minimum can be tested rather than trusted (see rippleReach).
//
// val is |sum of displacements|, and the absolute value is the design rather
// than a way to keep the sign positive. A dark pool is not lit by its height but
// by its slope, so a trough catches the light exactly as a crest does; taking
// the magnitude is what makes both of them bright and — the part that matters —
// what leaves a crest meeting a trough dark. Two rings that cancel sum to zero
// and stay black, which is interference rendered rather than simulated. A still
// pool is exactly 0, so it needs no offset and no floor: the absence of drops is
// the absence of light.
//
// aux is the contribution-weighted mean of the contributors' ages, and the
// weighting is the whole of it. "The strongest contributor's age" was the
// obvious rule and flips discontinuously: the carrier oscillates, so every ring
// has nodes where its own |contribution| passes through zero, and inside an
// overlap the winner changes at every one of those nodes — hue would flicker in
// bands through exactly the regions this variant is most interested in. The
// weighted mean has the same limits wherever one drop dominates, and blends
// where none does. It is 0/0 only where nothing contributes, where val is 0 and
// no colour is emitted at all.
func splashRippleSum(dx, dy, phase float64, reach, epochs int) (val, aux float64) {
	i0 := int(math.Floor(dx / rippleCell))
	j0 := int(math.Floor(dy / rippleCell))
	e0 := int(math.Floor(phase / ripplePeriod))

	sum, wsum, tsum := 0.0, 0.0, 0.0
	for di := -reach; di <= reach; di++ {
		for dj := -reach; dj <= reach; dj++ {
			for n := 0; n < epochs; n++ {
				c, t := rippleDropWave(i0+di, j0+dj, e0-n, dx, dy, phase)
				if c == 0 {
					continue
				}
				w := math.Abs(c)
				sum += c
				wsum += w
				tsum += w * t
			}
		}
	}
	if wsum == 0 {
		return 0, 0 // still pool
	}
	return clamp01(math.Abs(sum)), clamp01(tsum / wsum)
}
