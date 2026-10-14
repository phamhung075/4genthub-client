package clientruntime

// The daemon's agent-activity vocabulary, and the ONE word that may release a wait whose failure destroys a turn.
//
// THE SET IS THE RULE, NOT THE LIST. Both readers used to compare against the word they expected: the waiter tested
// `!= "running"`, which ACCEPTED every word it had never seen - `working`, `needs_input`, and any word the daemon adds
// later - so a reseat could proceed past a seat that is mid-turn. An allow-set of one answers the question the
// predicate is actually asking ("did the read say idle?") and refuses by default, so a vocabulary growth fails the
// test that pins this set instead of quietly authorizing a destroyer.
//
// WHY ONLY `idle`, grounded rather than chosen: the daemon's own taxonomy carries the human-facing values
// working | idle | needs-input | unknown and aliases its stored `idle-at-prompt` to `idle` (activity-taxonomy.js),
// which is the word that reaches this client in `agentActivity.state`. `unknown` is not evidence (the daemon's own
// comment says so), `working` is the opposite of the answer, and `needs_input` means the seat sits mid-turn waiting
// for a person - releasing a reseat there destroys a turn that was about to continue. Stillness may DELAY a
// destroyer; it must never satisfy one.
var quietActivityWords = map[string]bool{
	"idle": true,
}

// ActivityReleasesTheWait reports whether a word read from the seat's activity report says "not mid-turn", and so may
// release a wait that ends in a reseat or a compaction. Everything else releases nothing: the empty word, a word
// nobody defines, and every word this set does not name. The second answer of the read (did it happen at all) is the
// caller's to keep - an unreadable activity is not the answer "not running", it is the absence of an answer.
func ActivityReleasesTheWait(word string) bool {
	return quietActivityWords[word]
}
