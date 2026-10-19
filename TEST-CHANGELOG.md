# Test Suite Changelog

Track test suite changes, fixes, and improvements for the client.

## 2026-10-19 - the watch feed has a timing gate (branch 4genthub-min/perf-06-feed-timing)

- `internal/clientwatch/feed_timing_test.go` (new): `TestFeedWritesEachLineWithinOnePoll` starts
  `runFeed` against a temp state root and a fake tmux, waits for the startup replay, then appends
  one distinguishable event line to the seat's session log and requires the rendered marker at the
  writer within 2 x `feedPollInterval`, carried by a `Write` of its own. A buffered writer in front
  of `out` holds the line until its buffer fills or the process ends, so the bound fails; GREEN in
  1.0 s on 2026-10-19.
