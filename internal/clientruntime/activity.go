package clientruntime

// The daemon's agent-activity vocabulary: the ONE word that may release a wait whose failure destroys a turn, the
// words this client RECOGNIZES, and the alias joining the stored form to the served one.
//
// THE SET IS THE RULE, NOT THE LIST. Both readers used to compare against the word they expected: the waiter tested
// `!= "running"`, which ACCEPTED every word it had never seen - `working`, `needs_input`, and any word the daemon adds
// later - so a reseat could proceed past a seat that is mid-turn. An allow-set of one answers the question the
// predicate is actually asking ("did the read say idle?") and refuses by default, so a vocabulary growth fails the
// test that pins this set instead of quietly authorizing a destroyer.
//
// WHY ONLY `idle`, grounded rather than chosen: the daemon's taxonomy (daemon/dist/domain/activity-taxonomy.js)
// STORES three values - working | idle-at-prompt | unknown - and derives the fourth human-facing one, needs-input,
// from the seat's own state, so `needs-input` is not a word a producer emits. `unknown` is not evidence (the
// daemon's own comment says so), `working` is the opposite of the answer, and `needs_input` means the seat sits
// mid-turn waiting for a person - releasing a reseat there destroys a turn that was about to continue. Stillness may
// DELAY a destroyer; it must never satisfy one.
var quietActivityWords = map[string]bool{
	"idle": true,
}

// knownActivityWords is the vocabulary this client RECOGNIZES. Membership says nothing about releasing a wait - only
// `idle` does - it says that a refusal here is a DELIBERATE answer rather than a word nobody has seen. Every member
// is named by the daemon's own taxonomy: working | idle | needs-input | unknown, plus `running`, the STATUS word the
// old `!= "running"` comparison was written around, and `idle-at-prompt`, the STORED form of `idle`.
var knownActivityWords = map[string]bool{
	"idle": true, "working": true, "needs_input": true, "unknown": true, "running": true, "idle-at-prompt": true,
}

// normalizeActivityWord maps the vocabulary's synonyms onto the word the allow-set is written in.
//
// WHICH FORM THIS CONSUMES, named because the alias is exactly where a gate goes silent: activity-taxonomy.js stores
// `idle-at-prompt` and aliases it to `idle` before the human-facing surface reads it (the bridge maps stored -> served
// at activity-taxonomy.js:48-50), so the SERVED form is `idle` and the STORED form is not the string the set is
// written in. MEASURED on the served field tonight: `idle` arrives and `idle-at-prompt` does not - the alias is the
// daemon's own synonym, kept here so a daemon that ever serves the stored word cannot turn a genuinely idle seat into
// a silent refusal. Trusting that mapping to stay put would make a one-member set refuse the stored form - and that
// refusal is SILENT: an idle seat would read exactly like a dead gate, no quiet, forever, nothing said, which is the
// failure this row lived with for the other reason (the read supplying no word at all). So this client normalizes the
// alias itself, deliberately, and its test asserts the alias RELEASES; a synonym left neither normalized nor
// refused-on-purpose is how a gate never opens.
func normalizeActivityWord(word string) string {
	if word == "idle-at-prompt" {
		return "idle"
	}
	return word
}

// ActivityReleasesTheWait reports whether a word read from the seat's activity report says "not mid-turn", and so may
// release a wait that ends in a reseat or a compaction.
//
// THE SECOND VALUE MAKES THE REFUSAL LEGIBLE, and it is not decoration: it NAMES the word when the answer was a
// refusal by UNRECOGNITION, and is empty when the word was recognized - whether it released or not - and when the
// reading carried no word at all. Without it, a vocabulary growth is indistinguishable from a dead gate: the wait
// simply never opens and nothing says why. The read's own second answer (did it happen at all) stays the caller's to
// keep: an unreadable activity is not the answer "not running", it is the absence of an answer.
func ActivityReleasesTheWait(word string) (releases bool, unrecognized string) {
	normalized := normalizeActivityWord(word)
	if quietActivityWords[normalized] {
		return true, ""
	}
	if word == "" || knownActivityWords[normalized] {
		return false, ""
	}
	return false, word
}
