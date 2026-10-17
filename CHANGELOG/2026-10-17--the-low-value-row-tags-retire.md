# The four low-value row tags retire, and the refusal names the list that remains

The rail's tag list carried every kind of row ever written, and four of them - `status`, `ack`,
`checkpoint`, `report` - were propped up by habit: a row tagged `status` says a seat is working, which the row's
state already says, and the destination acts on none of the four. Measured over 12h: 311 rows on the rail, about
30 of them carrying one of these four. The daemon accepted them and the client's help offered them.

### Changed

- **The daemon's `ALLOWED_TAGS` (`rust/agenthub-runtime/src/queue/mod.rs`) is 14 entries.** A create naming one
  of the four is refused by the rule that already refused `urgent`/`GATE`: the error repeats the tag and the
  whole list that remains, so a seat is pointed at what it may use instead of guessing.
- **The client's `clientruntime.QueueTags` is the same 14**, and the help a seat reads (`room queue` usage) no
  longer offers the four - a usage line that listed one would send the seat to a refusal.
- **A row written before the retirement keeps its tag.** The refusal is on `create`, not on read or update: the
  rail is a record, and the rule decides what may be added. Measured on the wire: a row seeded with `status`
  lists back with it; the four creates above are refused with the 14-tag list.

### Testing

- Daemon: `the_retired_low_value_tags_are_refused_by_name` (each of the four refused, the refusal naming it,
  and absent from `ALLOWED_TAGS`) and `a_retired_tag_stays_on_a_row_that_already_carries_it` (a row inserted the
  way an older store holds it survives `list` intact). Red first: the first case failed on
  `err.contains("tag rule")` while the tags were still accepted.
- Client: `TestTheRetiredTagsLeaveTheListAndTheUsage` pins `QueueTags` as the 14 and asserts the usage offers
  none of the four. Red first: `QueueTags` still had 18.
- Two rust fixtures moved off the retired tag (`chain_of_record_and_reply_to_round_trip` and
  `high_requires_the_prod_break_tag` used `status` as an incidental tag); their subject is lineage and the
  priority rule, not the tag set.
- At this change: `cargo test` in `rust/agenthub-runtime` = 160 + 15 + 1 + 1 passed, 0 failed; `go test ./...`
  over the client module green. Smoke: the built daemon on a throwaway state root refused
  `status`/`ack`/`checkpoint`/`report` by name over its socket and accepted `gate`/`finding`.

### What this does not do

- It does not rewrite or migrate rows already carrying a retired tag, and does not filter them out of a list.
- It does not touch the `prod-break` priority rule or any other tag.
