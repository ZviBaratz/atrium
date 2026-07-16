package splash

import (
	"math"
	"testing"

	"github.com/muesli/termenv"
	"github.com/stretchr/testify/require"
)

// rippleTestPhases are phases spread across several spawn epochs, including the
// very first frames — where the field is only populated at all because the sum
// reaches back into negative epochs.
var rippleTestPhases = []float64{0, 5 * driftPerFrame, 0.9, 2.7, 4.5, 18.3}

// TestSplashRippleAtRange holds the point-fn contract every field shares: both
// returns inside [0,1] and neither ever a NaN. It is not merely hygiene here —
// aux is a ratio of two sums (see splashRippleSum), so the still pool is a live
// 0/0 that has to be answered rather than computed, and an int() of a NaN
// downstream is implementation-defined in Go rather than a crash.
func TestSplashRippleAtRange(t *testing.T) {
	for _, phase := range rippleTestPhases {
		for dy := -70.0; dy <= 70; dy += 1.3 {
			for dx := -130.0; dx <= 130; dx += 1.3 {
				val, aux := splashRippleAt(0, 0, dx, dy, phase)
				require.Falsef(t, math.IsNaN(val) || math.IsNaN(aux),
					"NaN at (%v,%v) phase %v: val=%v aux=%v", dx, dy, phase, val, aux)
				require.GreaterOrEqual(t, val, 0.0)
				require.LessOrEqual(t, val, 1.0)
				require.GreaterOrEqual(t, aux, 0.0)
				require.LessOrEqual(t, aux, 1.0)
			}
		}
	}
}

// TestRippleDropsStayInTheirCellAndEpoch pins the two invariants the whole
// early-out rests on, and neither is visible from the place that spends them.
//
// A drop must land strictly inside its own lattice cell, or rippleCell stops
// being the exact reach and the 3x3 window starts clipping rings near the cell
// boundaries — silently, and only for some drops. A drop must be born strictly
// inside its own epoch's window, or two epochs stop covering every live drop.
// Both hold only because splashCellHash returns [0,1), which is a property of
// the hash rather than of this file.
func TestRippleDropsStayInTheirCellAndEpoch(t *testing.T) {
	for i := -3; i <= 3; i++ {
		for j := -3; j <= 3; j++ {
			for e := -3; e <= 3; e++ {
				px, py := rippleDropPos(i, j, e)
				ts := rippleDropBirth(i, j, e)
				require.GreaterOrEqualf(t, px, float64(i)*rippleCell, "drop (%d,%d,e%d) x below its cell", i, j, e)
				require.Lessf(t, px, float64(i+1)*rippleCell, "drop (%d,%d,e%d) x above its cell", i, j, e)
				require.GreaterOrEqualf(t, py, float64(j)*rippleCell, "drop (%d,%d,e%d) y below its cell", i, j, e)
				require.Lessf(t, py, float64(j+1)*rippleCell, "drop (%d,%d,e%d) y above its cell", i, j, e)
				require.GreaterOrEqualf(t, ts, float64(e)*ripplePeriod, "drop (%d,%d,e%d) born before its epoch", i, j, e)
				require.Lessf(t, ts, float64(e+1)*ripplePeriod, "drop (%d,%d,e%d) born after its epoch", i, j, e)
			}
		}
	}
}

// TestSplashRippleLifeFitsTheEpochWindow pins the inequality rippleEpochs == 2
// is derived from. A drop of epoch e is born inside [e*P, (e+1)*P), so reaching
// back one epoch covers births down to (e0-1)*P; a drop stays alive for
// rippleLife, so the oldest live birth is at phase - rippleLife. Two epochs
// suffice exactly when rippleLife <= ripplePeriod. Retuning either constant past
// the other truncates the oldest rings — which is a ring vanishing mid-flight,
// not an error.
func TestSplashRippleLifeFitsTheEpochWindow(t *testing.T) {
	require.LessOrEqual(t, float64(rippleLife), float64(ripplePeriod),
		"rippleEpochs == 2 only covers every live drop while a drop dies within one spawn period")
}

// TestSplashRippleWindowIsExact is the geometric claim the point function's cost
// rests on, run rather than read: summing a far wider window must change nothing
// at all.
//
// Exactly nothing — the assertion is equality, not a tolerance, and that is the
// design being tested rather than a lucky rounding. The packet is a compact
// polynomial with a root at the packet edge, so a drop outside the window does
// not contribute a small amount, it contributes a term that is identically zero.
// A Gaussian or a rational tail would make this a threshold and this test a
// judgement call.
//
// The epoch half of the argument fails loudly here: raise rippleLife past
// ripplePeriod and epoch e0-2 comes alive, which this catches at +0.1.
//
// The lattice half does not, and the asymmetry is worth stating rather than
// leaving for the next person to trip over. Shrinking rippleCell below
// rippleMaxR + rippleW does let drops two cells out reach in — but only the ones
// whose crest is near its maximum radius, and a crest is only there at the very
// end of a life. That confines the leak to a sliver of the (age, position)
// space, and the sliver's *measure* is what decides this test: at a 1-unit
// shrink a leak exists only in the last 3.8% of a life and inside an annulus at
// most 1 unit wide, and a uniform sweep does not land there. At 2 units both
// halves double — the last 7.6% of a life, a 2-unit annulus — so the sliver is
// ~4x larger and the sweep does find it. Measured: catches 2, sails through 1.
//
// The leak's magnitude is deliberately not the argument, because it cannot be:
// the assertion is exact equality, so a leak of any size fails the instant a
// sample lands on one. (For scale, the largest a 1-unit shrink can leak is
// 1.2e-4 — fade = 1 - t^2 has taken the amplitude to almost nothing exactly
// where the crest is finally far enough out to reach. The ~1.6e-3 available at
// a 2-unit shrink is the caught case, not the missed one.)
//
// TestSplashRippleCellHoldsThePacketsReach is what holds this half, by
// construction rather than by sampling.
func TestSplashRippleWindowIsExact(t *testing.T) {
	checked := 0
	for _, phase := range rippleTestPhases {
		for dy := -70.0; dy <= 70; dy += 0.9 {
			for dx := -130.0; dx <= 130; dx += 0.9 {
				val, aux := splashRippleSum(dx, dy, phase, rippleReach, rippleEpochs)
				wideV, wideA := splashRippleSum(dx, dy, phase, rippleReach+2, rippleEpochs+3)
				require.Equalf(t, wideV, val, "val at (%v,%v) phase %v: the 3x3x2 window missed a drop", dx, dy, phase)
				require.Equalf(t, wideA, aux, "aux at (%v,%v) phase %v: the 3x3x2 window missed a drop", dx, dy, phase)
				checked++
			}
		}
	}
	require.Greater(t, checked, 100000, "the sweep has to be dense enough to land near cell boundaries")
}

// TestSplashRippleCellHoldsThePacketsReach is the lattice half of the exactness
// argument, held by construction where the sweep in TestSplashRippleWindowIsExact
// can only sample for it.
//
// The claim rippleCell has to earn is that one lattice cell is at least as wide
// as a drop's disturbance can ever be — that is the entire reason a drop two
// cells away is *provably* unable to touch a point, and so the entire reason the
// 3x3 window is a proof rather than a good approximation.
//
// So it measures the reach instead of restating it: sweep a drop's whole life,
// walk out until the wave function goes to zero and stays there, and take the
// furthest non-zero it ever produced. Comparing that measurement against
// rippleCell is a real question, where `require.Equal(rippleCell, rippleMaxR +
// rippleW)` would only be the constant agreeing with its own definition. It is
// also the guard that survives the packet being reshaped: give the envelope a
// tail — a Gaussian, a rational — and the measured reach runs off past the cell
// while every constant in the file still reads exactly as it does today.
func TestSplashRippleCellHoldsThePacketsReach(t *testing.T) {
	px, py := rippleDropPos(0, 0, 0)
	ts := rippleDropBirth(0, 0, 0)

	// Past the nominal support there must be nothing at all, so walking out to
	// it and a margin beyond finds the true edge rather than a node in the
	// carrier (which is zero, but has live wave on both sides of it).
	const beyond = 12.0
	maxReach, atAge := 0.0, 0.0
	for age := 0.0; age <= rippleLife; age += 0.002 {
		phase := ts + age
		for d := rippleMaxR + rippleW + beyond; d > 0; d -= 0.002 {
			if c, _ := rippleDropWave(0, 0, 0, px+d, py, phase); c != 0 {
				if d > maxReach {
					maxReach, atAge = d, age
				}
				break
			}
		}
	}
	require.Positive(t, maxReach, "the sweep found no wave at all")
	require.LessOrEqualf(t, maxReach, float64(rippleCell),
		"a drop's disturbance reaches %.3f units (at age %.3f) but the spawn lattice is "+
			"pitched at only %.3f — a drop two cells away can touch this point, so the "+
			"3x3 window in splashRippleSum silently clips rings instead of being exact",
		maxReach, atAge, float64(rippleCell))

	// And the pitch must not be wildly slack either: a cell far wider than the
	// reach would still be correct, but it would be spending drop density (one
	// drop per cell per period) on nothing. The reach is the pitch, near enough.
	require.Greaterf(t, maxReach, 0.95*float64(rippleCell),
		"rippleCell (%.3f) is much wider than a drop can actually reach (%.3f): the "+
			"lattice is the density knob, so slack here is drops the pool never gets",
		float64(rippleCell), maxReach)
}

// TestSplashRipplePacketIsCompactAndSigned pins the two properties of the wave
// packet that everything else is built on top of.
//
// Compact: exactly zero at and beyond the packet edge, which is what makes
// rippleCell an exact reach (see TestSplashRippleWindowIsExact) rather than a
// tolerance.
//
// Signed: the packet must go negative, and deeply enough for two rings meeting
// out of phase to cancel rather than merely dent each other.
//
// What this catches outright is the packet losing its carrier and becoming a
// plain positive bump — at which point |sum| == sum, nothing can ever cancel,
// and the field still renders perfectly plausible expanding rings while every
// other test passes.
//
// The depth floor is a tuning pin, not a cliff, and the difference is worth
// stating because the intuitive story is wrong: "below some cycle count the
// trough falls under the envelope's root and the packet goes positive" does not
// happen. Measured, the minimum is a smooth 15% of the crest at rippleCyc 1.0,
// 29% at 1.25, 41% at 1.5 — interference never switches off, it just fades. Even
// the field-level guard (TestSplashRippleCombinesBySignedSum) still finds
// cancelling cells at 1.0, so this is the only place the choice is held.
func TestSplashRipplePacketIsCompactAndSigned(t *testing.T) {
	// A drop we can address: cell (0,0), epoch 0, sampled along +x from its own
	// centre at an age where its ring has fully opened.
	px, py := rippleDropPos(0, 0, 0)
	ts := rippleDropBirth(0, 0, 0)
	const age = 1.4
	phase := ts + age
	rr := rippleSpeed * age

	// Compact: nothing at or past the packet's outer edge.
	for d := rr + rippleW; d < rr+rippleW+30; d += 0.25 {
		c, _ := rippleDropWave(0, 0, 0, px+d, py, phase)
		require.Zerof(t, c, "the packet must be exactly zero at %v units from the crest (edge is %v)", d-rr, float64(rippleW))
	}
	// And nothing inside the ring's hole, which is the same root on the other side.
	for d := 0.0; d <= rr-rippleW; d += 0.25 {
		c, _ := rippleDropWave(0, 0, 0, px+d, py, phase)
		require.Zerof(t, c, "the ring's hole must be exactly zero at %v units from the crest", d-rr)
	}

	// Signed: both a crest and a trough inside the support.
	lo, hi := 0.0, 0.0
	for d := rr - rippleW; d <= rr+rippleW; d += 0.05 {
		c, _ := rippleDropWave(0, 0, 0, px+d, py, phase)
		lo, hi = math.Min(lo, c), math.Max(hi, c)
	}
	require.Greater(t, hi, 0.2, "the packet needs a crest worth summing")
	require.Lessf(t, lo, -0.3*hi,
		"the packet's trough must reach 30%% of its crest for rings to cancel visibly; "+
			"at rippleCyc %v it reaches %.0f%% (crest %.3f, trough %.3f). A packet with "+
			"no trough at all is a bump, and bumps cannot interfere",
		float64(rippleCyc), 100*-lo/hi, hi, lo)
}

// TestSplashRippleCrestTravelsAtRippleSpeed is the variant's reason to exist,
// stated as a property of one drop: the disturbance is a ring, and the ring
// expands. It samples rippleDropWave rather than the summed field on purpose —
// in the field a neighbouring drop routinely out-peaks an old faint ring along
// the same ray, so an argmax over the sum measures the neighbourhood rather than
// this drop.
func TestSplashRippleCrestTravelsAtRippleSpeed(t *testing.T) {
	px, py := rippleDropPos(0, 0, 0)
	ts := rippleDropBirth(0, 0, 0)
	for _, age := range []float64{0.4, 0.9, 1.4, 1.9, 2.3} {
		phase := ts + age
		peak, at := -1.0, 0.0
		for d := 0.0; d < rippleMaxR+rippleW; d += 0.01 {
			c, _ := rippleDropWave(0, 0, 0, px+d, py, phase)
			if v := math.Abs(c); v > peak {
				peak, at = v, d
			}
		}
		require.InDeltaf(t, rippleSpeed*age, at, 0.05,
			"at age %v the crest must sit at rippleSpeed*age", age)
		require.Greaterf(t, peak, 0.0, "the ring must still carry amplitude at age %v", age)
	}
}

// TestSplashRippleCrestSurvivesTheRowPitch is the grid guard, and it is the one
// this variant was most exposed to.
//
// A column step moves dx by 1 but a row step moves dy by cellAspect, so the
// vertical axis samples this field at half the horizontal rate — and unlike the
// tunnel's, ripple's exposure cannot be mipped away, because its spatial
// frequency never compresses: the only defence is a crest wide enough that the
// row grid cannot fall between it and its neighbour. Rain shipped the failure
// this prevents — a head lobe spanning 43% of a row was caught only when a row
// happened to land on it, so over half of all heads rendered with no bright cell
// at all and the stream blinked.
//
// So: sweep every alignment of the row grid against the ring, and require even
// the worst one to still land on most of the crest's true peak.
//
// At the shipped packet that worst case is 87.3%, and it is a closed form rather
// than a measurement: the grid steps x by cellAspect/rippleW = 0.2, so the worst
// alignment straddles the peak at x = ±0.1 and reads (1-0.1^2)^2*cos(0.15pi) of
// it. Age cancels out of the ratio — fade and flash scale both samples alike —
// which is why every age below reports the identical number. The ages are swept
// anyway so that the guard keeps telling the truth if the packet ever becomes a
// function of age; today they are one constant measured four times.
//
// The 75% floor therefore has real margin under it (87.3 against 75) rather than
// being fitted to the current numbers, and it still bites where it should:
// rippleW 6 gives 67% and rippleCyc 2.3 gives 74%.
func TestSplashRippleCrestSurvivesTheRowPitch(t *testing.T) {
	px, py := rippleDropPos(0, 0, 0)
	ts := rippleDropBirth(0, 0, 0)
	for _, age := range []float64{0.4, 0.9, 1.4, 1.9} {
		phase := ts + age

		truePeak := 0.0
		for d := 0.0; d < rippleMaxR+rippleW; d += 0.01 {
			c, _ := rippleDropWave(0, 0, 0, px, py+d, phase)
			truePeak = math.Max(truePeak, math.Abs(c))
		}
		require.Greater(t, truePeak, 0.0)

		// The renderer only ever samples dy on a cellAspect grid; which offset it
		// lands on depends on where the drop fell, so every offset must work.
		worst := math.Inf(1)
		for off := 0.0; off < cellAspect; off += 0.1 {
			seen := 0.0
			for d := off; d < rippleMaxR+rippleW; d += cellAspect {
				c, _ := rippleDropWave(0, 0, 0, px, py+d, phase)
				seen = math.Max(seen, math.Abs(c))
			}
			worst = math.Min(worst, seen/truePeak)
		}
		require.Greaterf(t, worst, 0.75,
			"at age %v the worst row alignment sees only %.0f%% of the crest — the ring "+
				"will blink as it crosses rows (band period is %v units, %v rows)",
			age, 100*worst, float64(rippleW)/rippleCyc, float64(rippleW)/rippleCyc/cellAspect)
	}
}

// TestSplashRippleCombinesBySignedSum is the |sum| decision, pinned against the
// three rules it could plausibly have been.
//
// A dark pool is lit by its slope, so a trough catches light exactly as a crest
// does — hence the magnitude. But the magnitude is taken *after* the sum, and
// that ordering is the design: two waves meeting out of phase have to cancel and
// leave the water dark. Summing magnitudes (|c1|+|c2|) or taking the brightest
// contributor (max|c|) would both light that cell instead, and both would render
// as plausible expanding rings — so this finds a cell where the rules actually
// disagree and pins the one that ships.
//
// The enumeration is the test's own, over the same exposed primitives the field
// uses; what it asserts is the combination, which is what is under test.
func TestSplashRippleCombinesBySignedSum(t *testing.T) {
	cancels := 0
	for _, phase := range rippleTestPhases {
		for dy := -60.0; dy <= 60 && cancels < 40; dy += 0.6 {
			for dx := -60.0; dx <= 60 && cancels < 40; dx += 0.6 {
				i0 := int(math.Floor(dx / rippleCell))
				j0 := int(math.Floor(dy / rippleCell))
				e0 := int(math.Floor(phase / ripplePeriod))

				sum, absSum, maxAbs, n := 0.0, 0.0, 0.0, 0
				for di := -1; di <= 1; di++ {
					for dj := -1; dj <= 1; dj++ {
						for k := 0; k < 2; k++ {
							c, _ := rippleDropWave(i0+di, j0+dj, e0-k, dx, dy, phase)
							if c == 0 {
								continue
							}
							sum += c
							absSum += math.Abs(c)
							maxAbs = math.Max(maxAbs, math.Abs(c))
							n++
						}
					}
				}
				val, _ := splashRippleAt(0, 0, dx, dy, phase)
				require.InDeltaf(t, clamp01(math.Abs(sum)), val, 1e-12,
					"the field must be |sum of displacements| at (%v,%v) phase %v", dx, dy, phase)

				// Only a cell where the rules disagree proves anything.
				if n >= 2 && maxAbs > 0.1 && math.Abs(sum) < maxAbs-0.05 {
					cancels++
					require.Lessf(t, val, math.Min(1, absSum)-0.04,
						"waves must cancel rather than pile up at (%v,%v) phase %v", dx, dy, phase)
					require.Lessf(t, val, math.Min(1, maxAbs)-0.04,
						"a cancelling cell must be darker than its strongest single wave at "+
							"(%v,%v) phase %v", dx, dy, phase)
				}
			}
		}
	}
	require.Greaterf(t, cancels, 20,
		"only %d cells were found where rings cancel — with so few, this variant's "+
			"interference is not actually happening", cancels)
}

// TestSplashRippleHueIsTheWeightedRingAge pins what aux is, and the weighting is
// the whole of it.
//
// "The strongest contributor's age" was the obvious rule and is discontinuous:
// the carrier oscillates, so every ring has nodes where its own contribution
// passes through zero, and inside an overlap the winner changes at every one of
// those nodes — hue would flip in bands through exactly the cells this variant
// is most interested in. The weighted mean agrees wherever one drop dominates
// and blends where none does.
func TestSplashRippleHueIsTheWeightedRingAge(t *testing.T) {
	blended := 0
	for _, phase := range rippleTestPhases {
		for dy := -60.0; dy <= 60 && blended < 30; dy += 0.6 {
			for dx := -60.0; dx <= 60 && blended < 30; dx += 0.6 {
				i0 := int(math.Floor(dx / rippleCell))
				j0 := int(math.Floor(dy / rippleCell))
				e0 := int(math.Floor(phase / ripplePeriod))

				wsum, tsum, best, bestT, n := 0.0, 0.0, 0.0, 0.0, 0
				for di := -1; di <= 1; di++ {
					for dj := -1; dj <= 1; dj++ {
						for k := 0; k < 2; k++ {
							c, ct := rippleDropWave(i0+di, j0+dj, e0-k, dx, dy, phase)
							if c == 0 {
								continue
							}
							w := math.Abs(c)
							wsum, tsum, n = wsum+w, tsum+w*ct, n+1
							if w > best {
								best, bestT = w, ct
							}
						}
					}
				}
				_, aux := splashRippleAt(0, 0, dx, dy, phase)
				if n == 0 {
					require.Zerof(t, aux, "the still pool carries no hue at (%v,%v)", dx, dy)
					continue
				}
				require.InDeltaf(t, tsum/wsum, aux, 1e-12,
					"aux must be the contribution-weighted mean ring age at (%v,%v) phase %v", dx, dy, phase)

				// A cell where the two rules disagree: the mean must not be the winner's.
				if n >= 2 && math.Abs(tsum/wsum-bestT) > 0.05 {
					blended++
				}
			}
		}
	}
	require.Greaterf(t, blended, 15,
		"only %d cells blended two rings' ages — without them this test cannot tell "+
			"the weighted mean from the strongest contributor", blended)
}

// TestSplashRippleOpensMidFlight guards the negative epochs, which is the one
// thing about the spawn lattice that has nothing to do with the geometry.
//
// A drop's epoch is floor(phase/ripplePeriod), so at frame 0 that is epoch 0 and
// every drop in it is either unborn or newly landed. Reaching back to epoch -1 —
// a birth time before the animation began — is what makes frame 0 a pool that
// has been raining for a while rather than a flat sheet that starts from
// nothing. The splash is often on screen for only a few seconds, so its first
// frames are most of what anyone ever sees.
func TestSplashRippleOpensMidFlight(t *testing.T) {
	const w, h = 160, 44
	cx, cyFocal := float64(w-1)/2, float64((h-1)/2)
	mature, lit := 0, 0
	for row := 0; row < h; row++ {
		dy := (float64(row) - cyFocal) * cellAspect
		for col := 0; col < w; col++ {
			val, aux := splashRippleAt(col, row, float64(col)-cx, dy, 0)
			if val > 0.1 {
				lit++
				// A ring that has been travelling since before frame 0.
				if aux > 0.35 {
					mature++
				}
			}
		}
	}
	require.Greaterf(t, lit, 200, "frame 0 must not be an empty pool (only %d lit cells)", lit)
	require.Greaterf(t, mature, 60,
		"frame 0 must open mid-flight: only %d of %d lit cells carry a ring older than "+
			"a third of its life, so the field is starting from nothing", mature, lit)
}

// TestSplashRippleReachesItsOwnField guards the nastiest silent failure in the
// variant surface. splashFieldAt's switch falls through to the fallback, so a
// ripple registered in the enum, the rotation, the names, the ops and both test
// maps — but missing that one case — renders rain's field wearing ripple's
// Pass-2 policy, and the contract loop (determinism, bounds, animation) is
// perfectly happy with that.
//
// It samples the point function, and that is load-bearing rather than
// incidental: ripple's ops differ from the fallback's on both fields, so two
// ops-applied renders differ whatever field is underneath them. The tunnel's
// version of this test compared renders and passed with its case deleted.
//
// Sampling rain is not arbitrary: it is what splashFieldAt's default arm returns,
// which is the whole point — this asks "am I getting the fallback's field". Move
// that arm to another variant without moving this probe and the test still passes
// while testing nothing about the fallback.
func TestSplashRippleReachesItsOwnField(t *testing.T) {
	const phase = 5 * driftPerFrame
	sample := func(v Variant) []float64 {
		at := splashFieldAt(v, 96)
		out := make([]float64, 0, 2*21*31)
		for row := -10; row <= 10; row++ {
			for col := -15; col <= 15; col++ {
				val, aux := at(col, row, float64(col), float64(row)*cellAspect, phase)
				out = append(out, val, aux)
			}
		}
		return out
	}
	require.NotEqual(t, sample(Rain), sample(Ripple),
		"ripple must reach splashRippleAt — a variant with no case in splashFieldAt "+
			"silently falls through to the fallback's field")
}

// TestSplashRippleIgnoresThePaneSize is the scaling decision, pinned where a
// future maxD parameter would break it.
//
// Ripple is a field of many drops, not one object, so it is drawn at absolute
// scale: a bigger pane covers more lattice cells and therefore shows more drops,
// which is what more window should buy. The tunnel is the opposite case and
// closes over maxD (see splashFieldAt). Scaling this one to the pane would also
// shrink the rings on a small pane, which is exactly where the row pitch has the
// least room to resolve them (see TestSplashRippleCrestSurvivesTheRowPitch).
func TestSplashRippleIgnoresThePaneSize(t *testing.T) {
	const phase = 3.3
	for _, maxD := range []float64{20, 96, 400} {
		at := splashFieldAt(Ripple, maxD)
		val, aux := at(4, 7, 11.5, -23, phase)
		want, wantAux := splashRippleAt(4, 7, 11.5, -23, phase)
		require.Equalf(t, want, val, "the field must not depend on the pane radius (maxD %v)", maxD)
		require.Equalf(t, wantAux, aux, "the hue must not depend on the pane radius (maxD %v)", maxD)
	}
}

// rippleFieldVals is the Pass-1 field over a pane, in the same frame the
// renderer walks — so a rendered cell can be asked what it was made of.
func rippleFieldVals(w, h, frame int) [][]float64 {
	cx, cyFocal := float64(w-1)/2, float64((h-1)/2)
	phase := float64(frame) * driftPerFrame
	vals := make([][]float64, h)
	for row := 0; row < h; row++ {
		vals[row] = make([]float64, w)
		dy := (float64(row) - cyFocal) * cellAspect
		for col := 0; col < w; col++ {
			vals[row][col], _ = splashRippleAt(col, row, float64(col)-cx, dy, phase)
		}
	}
	return vals
}

// rippleMeasurable reports whether a rendered cell can be read as this field's
// own brightness. Two exclusions, and both would otherwise be measured as if
// they were the field.
//
// The starfield overwrites a cell wholesale with the star's own run, which
// shadeStopGrid decodes as -1 because it is not in the shade grid — a star over
// a bright ring would read as a blank cell. And the edge vignette genuinely dims
// cells near the borders, by construction and for every variant, so it has to
// stay out of any measurement about *this* variant's envelope.
func rippleMeasurable(col, row, w, h int) bool {
	if starHash(col, row) > starThreshold {
		return false
	}
	mx := int(math.Max(1, float64(w)*edgeVignetteFrac)) + 1
	my := int(math.Max(1, float64(h)*edgeVignetteFrac)) + 1
	return col >= mx && col < w-mx && row >= my && row < h-my
}

// TestSplashRippleRendersTheFadeNotAThreshold is the Pass-2 half of the design,
// asserted on the bytes a terminal would receive rather than on the field.
//
// Both halves are needed: Pass 1 being right has never proved Pass 2 emits it —
// rain shipped ops.dimToRim declared and never read, and every brightness test
// it had was structurally blind to that because they all asserted Pass-1 math.
//
// The claim is that a drop dies by fading, and the threat is a contrast window
// with a floor: one erases everything below it, so a decaying ring would not fade
// out — it would vanish outright the moment its crest crossed the floor, taking
// the faint majority of every packet with it. The organic fields shipped exactly
// such a window (0.36 to 0.64) and ripple would have inherited it by default;
// V5 retired them and with them the window, so Pass 2 now runs one full-range
// curve for every field. This measures that it stayed full-range.
//
// The band's own floor is not arbitrary and is not the window: a cell stops
// rendering at val ~0.10 regardless, because the full-range curve is still a
// Hermite S-curve (there is no identity on this path — see renderSplashField)
// and it crushes 0.10 to lit 0.028, which the luminance gate in shadeAt blanks.
// That floor is the design working; the band sits above it so that what is
// measured is the curve.
func TestSplashRippleRendersTheFadeNotAThreshold(t *testing.T) {
	withColorProfile(t, termenv.TrueColor)
	const w, h, frame = 240, 60, 300
	stops, _ := shadeStopGrid(t, w, h, frame, splashTestPalette(), Ripple)
	vals := rippleFieldVals(w, h, frame)

	faint, faintLit := 0, 0
	for row := 0; row < h; row++ {
		for col := 0; col < w; col++ {
			if !rippleMeasurable(col, row, w, h) {
				continue
			}
			// The upper edge is the floor the retired organic window would have
			// erased to, kept as the band's edge so the measurement is unchanged
			// from when that window was a live alternative.
			const retiredWindowLo = 0.36
			if v := vals[row][col]; v > 0.12 && v < retiredWindowLo {
				faint++
				if stops[row][col] > 0 {
					faintLit++
				}
			}
		}
	}
	require.Greaterf(t, faint, 300, "not enough faint cells to measure (%d)", faint)
	require.Greaterf(t, float64(faintLit)/float64(faint), 0.98,
		"a decaying ring must fade rather than pop: %d of %d cells between the render "+
			"floor and the retired window's floor rendered blank, so Pass 2 is putting "+
			"a threshold under this field", faint-faintLit, faint)
}

// TestSplashRippleRendersDropsTheSameEverywhere is the envelope half of the same
// Pass-2 claim: cells carrying the same field value must render at the same
// brightness wherever they are.
//
// Pass 2 used to offer a radial dim — brightness falling with distance from the
// wordmark. On the nebula it read as a glow; here it would mean a drop landing
// near the edge of the pane is dimmer than an identical drop landing in the
// middle, a difference the picture cannot account for, since nothing in this
// field is further away than anything else. V5 deleted it, along with the fields
// that wanted it, so this no longer guards an opt-out — it guards that nothing
// *new* makes brightness a function of position. That is worth keeping precisely
// because of how the old one failed: an envelope declared and never read looks
// identical to one read and set to zero until you measure the pane.
//
// The val band is narrow on purpose. A wide one would compare the *means* of two
// differently-shaped distributions — if the far cells happened to skew toward
// the band's dim end the test would read that as an envelope — so the band is
// kept to a couple of luminance stops and the sample is made up from several
// frames instead. The old dim would have dropped the far band ~2 stops, which
// this separates comfortably.
func TestSplashRippleRendersDropsTheSameEverywhere(t *testing.T) {
	withColorProfile(t, termenv.TrueColor)
	const w, h = 240, 60
	pal := splashTestPalette()
	cx, cyFocal := float64(w-1)/2, float64((h-1)/2)
	maxD := math.Hypot(math.Max(cx, float64(w-1)-cx), math.Max(cyFocal, float64(h-1)-cyFocal)*cellAspect)

	nearSum, nearN, farSum, farN := 0.0, 0, 0.0, 0
	for _, frame := range []int{60, 300, 700, 1500} {
		stops, _ := shadeStopGrid(t, w, h, frame, pal, Ripple)
		vals := rippleFieldVals(w, h, frame)
		for row := 0; row < h; row++ {
			for col := 0; col < w; col++ {
				if !rippleMeasurable(col, row, w, h) || stops[row][col] < 0 {
					continue
				}
				if vals[row][col] < 0.42 || vals[row][col] > 0.48 {
					continue
				}
				d := math.Hypot(float64(col)-cx, (float64(row)-cyFocal)*cellAspect) / maxD
				switch {
				case d < 0.35:
					nearSum, nearN = nearSum+float64(stops[row][col]), nearN+1
				case d > 0.6:
					farSum, farN = farSum+float64(stops[row][col]), farN+1
				}
			}
		}
	}
	require.Greaterf(t, nearN, 30, "the near band needs enough equally-bright cells (got %d)", nearN)
	require.Greaterf(t, farN, 30, "the far band needs enough equally-bright cells (got %d)", farN)
	near, far := nearSum/float64(nearN), farSum/float64(farN)
	require.InDeltaf(t, near, far, 0.5,
		"identical drops must render identically wherever they land: cells of the same "+
			"field value mean stop %.2f near the wordmark (%d cells) against %.2f out at "+
			"the rim (%d) — an envelope is dimming by radius", near, nearN, far, farN)
}
