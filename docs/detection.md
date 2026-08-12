# Reading a detection result

```
session_id: ses_bob  confidence: 1.000  bits_recovered: 32/32
null peak:  0.188 (best score reached by a sequence that was never issued)
```

## What the numbers mean

**confidence** is how well the recovered sequence fits the winning session,
normalised to [0, 1].

**null peak** is the best score reached by sequences that were *never issued*.
It is measured on this file, under the same search the detector actually ran.
It is the score a wrong answer can reach by chance here.

**bits_recovered** is how many segment decisions survived, out of how many were
read.

A match is reported only when confidence clears both the threshold and the null
peak. If the two are close, the result is weak however high the confidence
looks on its own.

## Why the null peak matters

Searching for the watermark grid takes a maximum over thousands of candidate
alignments, and matching takes a maximum over every issued session. Both lift
the score that pure noise can reach. A threshold picked without measuring that
would report high confidence for content carrying no mark at all.

## What the detector refuses to do

- Unwatermarked content produces no attribution, rather than naming whichever
  session sits closest to the noise.
- A leak whose session is not in the issued list produces no match, even at
  confidence 1.000, rather than blaming the nearest innocent viewer.

## Limits you must state if you act on this

- **Not collusion resistant.** Two viewers who compare copies can splice around
  the mark. The eval harness measures this and reports it as an expected
  failure.
- **Accuracy is content dependent.** See the measured baseline in the README.
  One of the three tested content types currently attributes nothing.
- **Detection is evidence, not proof.** Publish your false-positive rate
  alongside any accusation. Run `sigil eval` to measure it for your content.
