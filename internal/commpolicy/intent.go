package commpolicy

// IntentWords is the CLI spelling of each intent. It lives here, beside the constants, because the ONE
// surface that takes `--intent` is the client's own send path (internal/clientseat, wired 2026-10-15: the
// architect's ruling that the decision attaches to the capability), and a word list copied into that package
// would be a second definition of the same five words.
var IntentWords = map[string]Intent{
	"task":       IntentTask,
	"escalation": IntentEscalation,
	"report":     IntentReport,
	"question":   IntentQuestion,
	"notice":     IntentNotice,
}

// IntentNames is those five words as the usage lines print them, in the same order.
const IntentNames = "task|escalation|report|question|notice"

// ParseIntent resolves one CLI word and reports whether it is one of the five. An unknown word is NOT
// defaulted: the intent picks the link kind that can authorize a send, so a guessed intent is a guessed gate.
func ParseIntent(word string) (Intent, bool) {
	intent, ok := IntentWords[word]
	return intent, ok
}
