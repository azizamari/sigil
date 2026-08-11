# Phase 1 spike — detection feasibility

Throwaway. Not merged, not built upon. Answers one question: after a re-encode,
is variant 0 distinguishable from variant 1 at all?

**Yes, decisively — for normally-textured content, at an amplitude that costs ~1% bitrate.**

## Method

Block-constant antipodal luma modulation: a 32×18 grid of 40px blocks, each
carrying a pseudorandom sign. Variant 0 adds `+amp·sign`, variant 1 adds
`−amp·sign`. Embedded with ffmpeg `grainmerge` on an isolated luma plane so no
YUV range conversion rescales the amplitude.

Extractor is deliberately crude: per-block mean, minus the 8-neighbour mean to
suppress scene content, correlated against the known sign grid. One score per
frame; decisions taken on the sign of a window mean.

`sep` is `(v0 − v1) / 2σ_null` — per-frame separation in units of the
unwatermarked noise floor. `acc` is the fraction of windows decided correctly,
over both variants, so 50% is chance.

```
go run ./spike                  # CRF packaging, amplitudes 2/4/6
go run ./spike -rung 1500       # fixed-bitrate ABR rung
```

## Results

Motion fixture, packaged at a 1500 kbps rung:

| amplitude | attack | sep | 1s acc | 3s acc | bitrate cost |
|---|---|---|---|---|---|
| 2 | re-encode 60% | 6.9 | 100% | 100% | +1.3% |
| 2 | 480p + re-encode 60% | 7.1 | 100% | 100% | |
| 2 | **crop 5% + re-encode 60%** | **0.5** | **50%** | **50%** | |
| 4 | re-encode 60% | 14.8 | 100% | 100% | +1.2% |
| 6 | re-encode 60% | 22.3 | 100% | 100% | +1.4% |

Screencast fixture (synthetic, collapses to 40 kbps):

| amplitude | attack | sep | 1s acc | 3s acc |
|---|---|---|---|---|
| 2 | re-encode 60% | 0.4 | 70% | 70% |
| 4 | re-encode 60% | 0.9 | 80% | 90% |
| 6 | re-encode 60% | 1.4 | 93% | 100% |
| 6 | crop 5% + re-encode 60% | 0.2 | 63% | 70% |

## What this does not establish

- **Alignment.** Every passing row above kept the block grid registered to the
  pixels it was embedded on. Crop 5% collapses detection to chance. This is
  SPEC open question 1 and risk 13.1, confirmed as the real wall, and it must be
  prototyped before the packager.
- **Real content.** Fixtures are synthetic. Flat colour compresses far better
  than real capture — x264 would not spend 1500 kbps on the screencast fixture
  even when asked, so its bitrate-overhead ratios are inflated and its
  robustness numbers are pessimistic in a way real screen capture may not be.
- **A null that is not zero-mean.** Unwatermarked content scores +0.41 (motion)
  and +1.18 (screencast), not 0: a fixed pattern correlated against fixed
  content gives a deterministic content-dependent bias. Sequence-level
  correlation should cancel it, since the bias is constant across segments while
  assigned bits vary — but a per-segment hard threshold would inherit it.
- Per-window accuracy is not system accuracy. Recovery aggregates over ~22
  segments with ECC, so 80% per-segment reliability is comfortably recoverable.

---

# Spike 2 — alignment

Spike 1 left crop as the open wall: 5% border removal dropped detection to
chance because the block grid no longer sat on the pixels it was embedded on.
This spike searches for the alignment instead of assuming it.

**Crop is recoverable. It was never a fundamental limit, only an unsolved one.**

```
go run ./spike -mode align -secs 8 -amps 2,4,6
```

A summed-area table per frame makes a block mean four lookups, so the whole
(crop, dx, dy) space is searched cheaply. Coarse pass then refine, maximising
|score| — the sign identifies the variant, so the peak is the largest magnitude
either way.

| amplitude | input | recovered crop | score | variant |
|---|---|---|---|---|
| 2 | v0 cropped | 5.75% | +2.833 | correct |
| 2 | v1 cropped | 5.00% | −2.097 | correct |
| 4 | v0 cropped | 5.00% | +4.856 | correct |
| 4 | v1 cropped | 5.00% | −4.375 | correct |
| 6 | v0 cropped | 5.00% | +7.173 | correct |
| 6 | v1 cropped | 5.00% | −6.693 | correct |
| any | **unwatermarked** | 12.75% | **−2.056** | — |

True crop was 5% per side. The search recovers it exactly at amplitude 4 and 6,
and the score returns to roughly the aligned-clean value from spike 1: alignment
recovery restores nearly the whole signal.

## The catch, which matters more than the result

**Searching raises the null floor.** Unwatermarked content peaks at |2.056|
because taking a maximum over thousands of candidate alignments is a
multiple-comparisons problem: the best of many noise draws is much larger than
any single draw. In spike 1, where alignment was assumed, the null sat at 0.41.

Two consequences for the detector:

- Amplitude 2 scores +2.833 / −2.097 against a null of 2.056. It recovers the
  right variant here, but it does not clear the null by any margin worth
  trusting. **Amplitude 4 or more is required once crop search is in play**,
  where amplitude 2 was ample when alignment was known.
- The confidence threshold must be measured *under the same search the detector
  actually runs*. A null distribution computed at fixed alignment understates
  the false-positive rate by roughly 5x and would turn the detector into an
  accusation generator.
