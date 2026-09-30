# Usage request review and ablation

The review of PR 239 is addressed against upstream main `c6ca0ec`.
Request matching and cross-period IO contention are fixed, while compressed
metadata substantially reduces retained memory. Full-history refreshes with four
large active sessions trade some latency for that lower residency. All numbers
below use synthetic data; no local conversation content or credentials are included.

## Behavior

- Requests content uses its own expansion/collapse translation keys. The agent
  model picker keeps its existing Show all translation. OFFICIAL is translated.
- The tray's Open Usage follows `mainView` and `mainURL` to Requests with the
  selected provider/agent. The upstream routing link fix is included.
- Model ranking clicks use an exact `model` filter, also on CSV export. Free-text
  search remains a substring search. The panel legend and chart show the same
  seven named categories, plus Other.
- Safari 15 has a solid badge background/border fallback. Five unused translations
  and the unused provider subscription identity method were removed.
- Successful request content remains cached. Missing/unreadable content is retried
  when the detail is opened or redrawn; a later-written source can become visible.
- Usage starts on Today, including the overview beside allowances. Sessions lists
  and statistics include Claude Desktop Cowork sessions.
- OpenAI's [rate card](https://help.openai.com/en/articles/11481834-chatgpt-rate-card-business-enterpriseedu-credit-based-pricing)
  explicitly states that Auto review uses GPT-5.6 Luna (checked 2026-09-30).
  The existing fallback remains an API list-price reference, not evidence of the
  model that served a particular request or a subscription billing rate.

## Implementation

Request IDs are matched first. Remaining candidates are indexed by native session,
agent, four token counts, failure state and presence of a request ID, then searched
by end time. Range counters preserve uniqueness in both directions, even for
thousands of identical retries, without enumerating every possible pair.

The shared Requests lock protects metadata lookup and publication. File IO,
parsing, pricing, compression, decompression and aggregation run on immutable
snapshots outside it. An obsolete snapshot cannot overwrite a newer cache key.
Gateway appends copy published storage before changing it.

Codex cumulative token counters are explicit exported fields so they survive gob
serialization. A regression reloads a shard between appends and checks both delta
tokens and repeated-total suppression. Cache version `calls-v3` rebuilds older
derived shards, including any inflated deltas written by the earlier format; raw
session files are unchanged.

Local snapshots use a 24 MiB retention budget. Request metadata uses a compact
variable-length encoding and fast DEFLATE; small chunks remain expanded. Unchanged
archives are reused. Gateway metadata remains compressed instead of being dropped
wholesale at the former 32 MiB expanded-size threshold. Gateway retention grows
with its compressed history; **it has no hard 32 MiB cap**. These are retention
policies, not a bound on peak process heap. Requests also releases the duplicate
expanded parser cache; append-frame shards retain parser continuation on disk.

Session fingerprints read at most sixteen distributed 4 KiB windows. Files up to
64 KiB are hashed in full. Both summary and request readers use this fingerprint;
old full-prefix fingerprints rebuild once. This is an explicit integrity/performance
tradeoff: a same-size or growing in-place rewrite outside all sampled windows can
be missed. Head/middle/tail sample rewrites, truncation and the existing small-file
same-prefix rewrite cases are covered. Gateway prefix validation still hashes its
full prefix.

## Scale comparison

Measured 2026-09-30 on an Apple M4 Pro, Go 1.27.1, darwin/arm64. Baseline is
`7d56120`'s request query and session fingerprint implementation, overlaid on the
same merged source and fixture. There are 2,000 session files across 90 days,
379,400 local calls (four sessions with 20,000 calls), and 50,000 gateway records.
The active phase appends to all four large Claude sessions for 12 refreshes.
Codex append-after-reload correctness is covered separately by the regression.
Reported write volume assumes a five-second UI cadence, without sleeping between cycles.

| Measure | Reviewed baseline | Final |
| --- | ---: | ---: |
| Month first query | 652.0 ms | 855.0 ms |
| All first query | 754.2 ms | 987.0 ms |
| Month unchanged query | 11.6 ms | 13.7 ms |
| All unchanged query | 12.2 ms | 14.8 ms |
| Month retained heap after GC | 141.15 MiB | 13.35 MiB |
| All retained heap after GC | 121.17 MiB | 18.38 MiB |
| Four active sessions mean refresh | 436 ms | 593 ms |
| Active sampled peak HeapInuse | 334.72 MiB | 264.40 MiB |
| Request cache writes per minute | 0.078 MiB | 0.081 MiB |

The active fixture has no gateway/local session matches, so it isolates refresh
and retention overhead rather than the matching speedup. Final active refreshes
are about 36% slower in this workload. Unchanged reads remain about 15 ms. These
are same-machine observations, not latency guarantees or subscription cost data.

## Ablations

Matching and fingerprint numbers are medians of three runs. Each matching run
uses one operation; fingerprint runs use 100 operations.

| Change removed | Final | Without the change |
| --- | ---: | ---: |
| Match index, 1,000 calls in one session | 0.609 ms | 47.828 ms |
| Match index, 4,000 calls | 1.494 ms | 752.257 ms |
| Match index, 8,000 calls | 3.000 ms | 3,003.856 ms |
| Match index, 8,000 identical timestamps | 1.621 ms | 4,698.983 ms |
| Sampled fingerprint, 10,350,000-byte file | 0.042 ms | 3.479 ms |
| Compact snapshot, 8,000 rows | 83,444 bytes | 1,630,941 expanded bytes |
| Release duplicate parser cache, month retained heap | 13.35 MiB | 93.18 MiB |
| Release duplicate parser cache, active mean refresh | 593 ms | 530 ms |

The snapshot size is accounted payload size, not process heap; encode/decode
medians are 0.820/1.305 ms. Dense timestamps remain ambiguous and produce zero
fallback matches in both implementations. A seeded equivalence test also checks
failures, IDs, native sessions, token counts and period boundaries.

The lock ablation deliberately blocks All inside source IO. Today completes with
the final implementation. Restoring a global IO lock makes the test fail with
`Today waited on All's blocked file IO` after two seconds. That failure is the
expected negative control; the runner requires it.

## Validation and reproduction

- Full `go test -tags nogui ./...` and `go vet ./...` pass.
- Sessions, usage and gateway race suites pass. GUI race tests pass when skipping
  `TestGatewayTakenOver`. That existing test also fails on untouched upstream
  `c6ca0ec`: its cleanup writes `gatewayWatch` while `watchGateway` reads it.
- macOS desktop and Linux/Windows amd64 CLI builds pass.
- 60 browser checks pass across Chromium/WebKit, English/Chinese, light/dark and
  narrow layouts, including the upstream agent model picker and panel routing.
- Installed the ad-hoc signed macOS build `pr239-review-20260930-r2` locally.
  Native checks cover Requests loading after the cache rebuild, per-call Codex
  deltas, the translated official badge, exact model filtering, and expanding
  request content. The installed binary matches the validated build.

Run all experiments sequentially, including the expected negative control:

```sh
python3 internal/gui/tests/usage-ablation.py --output /tmp/magpie-usage-ablation
```

The runner writes raw logs and Go overlays to the output directory, makes no
working-tree edits, and uses isolated synthetic homes. It needs baseline commit
`7d56120` in the repository. Matching's legacy arm is retained only in test code.
The 8,000-call dense legacy case temporarily allocates about 2 GiB per operation.
