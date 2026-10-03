# Native Claude message replay

`claude-native-replay.jsonl` contains three assistant events extracted from one real Claude Code session: message A, message B, then an older block of A carrying exactly the same whole-message usage. The original event order, usage values and relative timestamps are retained. Message/request/block/session IDs are replaced consistently, timestamps are shifted by a fixed offset, and content is replaced with a placeholder. Paths and other metadata are removed.

Expected totals for the two messages: input 10,338, output 37,363, cache read 281,066, cache write 63,398; total 392,165 tokens. Main at d5591b8f counts A again in the session summary, producing 587,744 tokens. The request reader already finds two messages; this test requires the summary and request readers to agree.

Original line references and the source hash remain local. The regression test also restarts between every pair of events and checks the derived cache contains no message/block/tool maps.
