# Native response identity fixture

The local A/B/A message excerpt comes from a real Claude JSONL log. Event order, usage fields and relative timestamps are retained. IDs are replaced consistently; timestamps use one fixed shift. Text is replaced with a placeholder and paths/account metadata are omitted. Original hashes and line references remain local.

The gateway records in the test are **simulated**, with the same message IDs, no request-header IDs, and a five-second observation delay. They exercise correlation of native local messages; they are not a captured end-to-end gateway trace. At main d5591b8f, the page and ledger both retain four rows (784,330 tokens). With response identity matching they both retain two (392,165 tokens). The fixture's two messages are already deduplicated by the main request reader; Claude summary replay handling is covered separately by #697.
