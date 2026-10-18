`complete` names why the task stayed open

The gate verdict and the remote steps were one surface: after the verdict line, an evidence POST
that failed printed its HTTP status and the verb returned; a failed completion printed its status and
returned, with the task's state unknown. Measured 2026-10-18: the gate accepted, the evidence POST
500'd, and the task stayed `in_progress` with nothing a seat could act on.

The verb now names the consequence after the gate line, in the enforced gate's own vocabulary ("the
task was not completed"):

- evidence write failed (any non-2xx except 409): `the task was not completed: the evidence was not
  stored; task <id> is unchanged` - no completion call is made, so the state is known without a read.
  The status and hub body stay on the `4genteam evidence:` lines.
- evidence already stored (409): `warning: head_sha <sha> already has evidence; completing on the
  stored submission` - the retry path; refusing here made a post-failure retry impossible.
- completion request failed: the task is read back once and named - `reads done: it completed`,
  `reads <status>`, or `the state could not be read: <err>`.

Exit codes are unchanged: 0, 5, 6, 1.

Changed files:
- `internal/clientevidence/evidence.go` - `Post` returns `PostResult{Exit, HTTPStatus}`;
  `postEvidence` builds it, and the `4genteam evidence` verb returns `.Exit`, so its surface and codes
  are byte-identical.
- `internal/clientcomplete/complete.go` - `fetchCriteria` becomes `readTask` (one GET: status and
  criteria); the consequence lines and the 409 continuation.
