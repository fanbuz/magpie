# Native Claude message replay

`claude-native-replay.jsonl` contains three assistant events extracted from one real Claude Code session: message A, message B, then an older block of A carrying exactly the same whole-message usage. The original event order, usage values and relative timestamps are retained. Message/request/block/session IDs are replaced consistently, timestamps are shifted by a fixed offset, and content is replaced with a placeholder. Paths and other metadata are removed.

Expected totals for the two messages: input 10,338, output 37,363, cache read 281,066, cache write 63,398; total 392,165 tokens. Main at d5591b8f counts A again in the session summary, producing 587,744 tokens. The request reader already finds two messages; this test requires the summary and request readers to agree.

Original line references and the source hash remain local. The regression test also restarts between every pair of events and checks the derived cache contains no message/block/tool maps.

## Active files larger than the revision budget

Each active file retains a least-recently-used window of at most 1,024 index entries, including message branches, block IDs, tools and previous revisions. This fits eight active files within the shared 8,192-entry budget. Eviction forgets the oldest message history while retaining the file offset and summary. One message that exceeds the window by itself retains its latest usage and forgets its older block/tool/revision history.

Within the window, the A/B/A and older-snapshot rules above apply. Beyond it, summaries use the original last-message behavior and calls can reuse their materialized row by compatible message/request identity; arbitrary historical replay reconciliation is not guaranteed. Inactivity eviction and restart still rebuild the transient window once when a file next changes.

`TestLargeClaudeFileKeepsAppendContinuation` writes 24,000 messages, verifies summary continuation and append-frame inode reuse across three appends, checks a recent replay and restart, and checks the window bounds. Additional tests cover one oversized message and reuse of call rows beyond the window. The optional `BenchmarkClaudeLargeActive` covers cold/append reads for summaries and calls, with both short records and about 960 MB of padded records.
