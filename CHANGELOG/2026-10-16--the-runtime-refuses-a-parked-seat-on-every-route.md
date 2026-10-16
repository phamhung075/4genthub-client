# The runtime refuses a parked seat on every route that hands it work

### Added
- `rust/agenthub-runtime/src/park.rs`: the runtime's own reading of a park record and the ONE sentence it
  answers with - the reason, the cost, the calls, the time and the record's path. The client's
  `internal/seatpark` is the friendlier sentence at the operator's own routes; this is the enforcement,
  because a seat command, the daemon's own CLI and any future caller all end at the same process, and a
  caller above the runtime can be forgotten while this one cannot.
- Four routes are gated, and they are the routes that hand a seat work or bring it back: `deliver_text`
  (every text-into-a-seat producer crosses it - `SendText`, the daemon's own `send`, and the tmux paste,
  whose only caller is `deliver_text`), `start_seat` (the route both `RestartSeat` and a room start use, so a
  parked seat's room start reports it instead of silently bringing one seat up), `restart_seat` (the refusal
  comes BEFORE the stop, so a refused restart has changed nothing), and `nudge_seat` (before the attempt log,
  so a parked seat spends no attempts of the ladder).
- `crate::park::refuse` takes the action the caller was about to take (`Nothing was sent`,
  `nothing was restarted`, `nothing was started`, `the rail prompt was not delivered`), so the refusal says
  what did not happen rather than only what stopped it.

### Where it speaks, and where it does not
- The record is unchanged and the breaker's: `<state root>/rooms/<room>/seats/<seat>/parked.json`, written by
  the budget breaker and removed by whoever decides the seat may work again. The runtime only reads it.
- A seat that is parked is not started and not stopped: a refused restart and a refused room start leave the
  seat exactly as it was, and the refusal is a transport error the caller sees, which is what makes a room
  start's own "started no seat" rule the sentence the operator reads.
- `stop_seat` is deliberately NOT gated: parking a seat stops it, and refusing the stop would leave a parked
  seat running while calling itself parked.

### Testing
- `cargo test --offline` in `rust/agenthub-runtime`: 130 unit + 7 + 1 integration, 0 failed.
- Red first, and the red is the pre-gate tree: `a_parked_seat_is_refused_by_the_runtimes_own_routes` answered
  `deliver_text: tmux paste-buffer failed: can't find pane` and `a_parked_seat_is_not_nudged` answered
  `no runner for lab/dev: cannot reach its mailbox ...`, i.e. the transport error the gate replaces with the
  park; restored, both pass.
- Probed against a scratch daemon (its own state root and socket, `/tmp/park-probe/probe.sh`): `SendText` and
  `RestartSeat` on a parked seat and a room start whose every seat is parked all answer with the park sentence,
  the UNPARKED seat in the same room answers with a transport error (the control that proves the refusal is
  the park and not a blanket failure), and `4genteam send` through that foreign socket passes the runtime's
  own refusal through to the operator.
