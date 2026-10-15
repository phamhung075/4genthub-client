# The withdrawal is withdrawn: the room's `logs/` is the runner's log

`0277556` and `4719f94` are withdrawn, and `2800dfb` stands. The ruling they were carried out under was withdrawn
by its own author sixty seconds before those two commits were stamped, and this entry records both the design
and the timing, because the second one is a process error and not a fact about the code.

## What moved it

The lead's correction (20:58) and the architect's ruling (21:01) held that `rooms/<room>/logs/` is out of the
room's layout and that the fix belongs in the harness. On that reading, `0277556` reverted `2800dfb` and
`4719f94` dropped the word from `room stop`'s prose.

At **21:08:08Z** the architect withdrew the ruling and re-derived it from the source of the claim itself:
`agenthub_client@cc0c62f` — the commit whose changelog sentence "The dead `logs/<seat>.jsonl` creation is
removed" both readers took as "the directory was retired" — **deletes the directory's creation in its own
diff**:

```
-        let logs_dir = room_dir.join("logs");
-        let _ = fs::create_dir_all(&logs_dir);
-        let transcript_path = logs_dir.join(format!("{}.jsonl", seat.seat_key));
+        let transcript_path = seat_transcript_path(&room_dir, &seat.seat_key);
```

So what that commit retired is the runtime's **writer of a transcript copy** under `logs/`, not the directory's
place in the layout: the native runtime created it at room start all along, and the two empty `logs/`
directories on this box are that deleted `create_dir_all`'s fossils (12:11–12:12, before `cc0c62f` at 17:58Z),
not something a harness wrote. The architect's own words: **"`2800dfb` is the fix and must land."**

The timing, plainly: the withdrawal row is stamped **21:08:08Z**; `0277556` and `4719f94` are stamped
**21:09:08Z**. The decision was taken against the ruling as it read at 21:01, and the queue was not re-read
between deciding and committing — which is the one cheap check that would have caught it. A ruling is the
state of the record at the moment of the write, not the moment of the read.

## The change

Both halves are restored: `rust/` is byte-identical to `2800dfb` (`git diff 2800dfb -- rust/` is empty) and the
`clientroom` prose is back to naming `spec.json, seats/ and logs/`, which is what `room stop` leaves in place.
The two entries that shipped the withdrawal are removed from the changelog tree, so the published record
describes the code that is there.

## The design as it stands

The room layout is `spec.json`, `seats/`, `logs/`, `run/`, `queue.sqlite` — with **two artifacts that must not
merge**:

- `logs/<seat>.jsonl` is the **runner's** log: `mirror`, the single door every runner diagnostic goes through,
  all 21 call sites (the retry ladder, the cap error, the refusals), appends one JSON line per mirrored line.
- `seats/<seat>/sessions/*.jsonl` is the **agent's** session and the one definition of the transcript
  (`supervisor::transcript_path` reads it; the runner only points the agent at that directory).

A transcript copy at `logs/` is exactly what `cc0c62f` retired and must not come back.

## Evidence

- `git diff 2800dfb -- rust/` empty; `git diff` against the pre-flip text of `internal/clientroom/` empty.
- `cargo test --offline` in `rust/agenthub-runtime`: **88 passed, 0 failed** — the room-layout test and the
  runner-log test are both back, beside everything the withdrawal left untouched.
- The one remaining line is **not** this branch's: B2's cap-string leg reads `$(victim_transcript)` (the
  session) where it should read the runner's log; that is the harness owner's change, per the architect's
  21:08 row.
- The pin from `ad03a16` and the rail unit (`427b674`, its own branch `4genthub-min/rail-per-request`) are
  unaffected; the rail branch was rebased onto this tip so it carries the restored state.
