package clientdecide

// THE OWNER'S QUESTION IS NOT THE LEAD'S TO REWORD. Measured on aa2-rs, 2026-10-27: the lead asked Jev at
// lead tier "Phase 1 depth-gate exit rule: ..." (08:29:08Z, escalated-to-owner), closed the owner-decision row
// it had raised, and asked again at lead tier as "Phase 1 exit rule scope" (08:45:37Z) and "Phase 1 exit rule:
// does short gate" (16:05:22Z) until Jev decided. The owner was never asked. So a lead-tier ask that reads as
// one already escalated to the owner, inside the window, is not sent to Jev: it answers "escalate to owner"
// again, by code, naming the open escalation. `--distinct-from-owner-ask <why>` lets a genuinely different
// question through, and the reason is written into the log entry so the override is visible.

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"
	"time"
	"unicode"

	"github.com/phamhung075/4genthub-client/internal/clientjev"
)

// ownerAskWindow is how long an escalation to the owner stays the owner's. It is longer than a working day
// on purpose: the owner may answer the next morning, and the lead's job meanwhile is to wait on it.
const ownerAskWindow = 7 * 24 * time.Hour

// ownerAskOverlap is the share of the SMALLER ask's words that must also be in the escalated one for the
// two to count as the same question; ownerAskMinWords keeps a two-word summary from matching everything.
const (
	ownerAskOverlap  = 0.6
	ownerAskMinWords = 3
)

// openOwnerAsk returns the newest lead-tier escalation to the owner (or, in a director room, to the director), inside the window, that the new ask
// rewords. It reads the Jev log the verb itself appends to; an unreadable log answers none, because the
// guard must not stop a decision on a file it could not read - the log write that follows reports that.
func openOwnerAsk(path string, now time.Time, summary string) (clientjev.LogEntry, bool) {
	file, err := os.Open(path)
	if err != nil {
		return clientjev.LogEntry{}, false
	}
	defer func() { _ = file.Close() }()
	asked := askWords(summary)
	var found clientjev.LogEntry
	ok := false
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1<<20), 1<<24)
	for scanner.Scan() {
		var entry clientjev.LogEntry
		if json.Unmarshal(scanner.Bytes(), &entry) != nil {
			continue
		}
		if entry.Tier != "lead" || (entry.Outcome != "escalated-to-owner" && entry.Outcome != "escalated-to-director") || now.Sub(entry.Timestamp) > ownerAskWindow {
			continue
		}
		if sameAsk(asked, askWords(entry.Summary)) {
			found, ok = entry, true
		}
	}
	return found, ok
}

// askWords is the set of content words of an ask's SUMMARY, lower-cased, words of three letters or more.
// The summary is the subject the skill asks for; question texts and options were measured against the
// whole Jev log (2026-10-27) and matched unrelated asks, while summaries alone caught both aa2-rs rewordings
// and nothing that was a different subject.
func askWords(summary string) map[string]bool {
	words := map[string]bool{}
	for _, word := range strings.FieldsFunc(strings.ToLower(summary), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if len([]rune(word)) >= 3 && !askStopWords[word] {
			words[word] = true
		}
	}
	return words
}

var askStopWords = map[string]bool{
	"the": true, "and": true, "for": true, "with": true, "what": true, "which": true, "does": true, "should": true,
	"this": true, "that": true, "from": true, "into": true, "now": true, "are": true, "can": true, "not": true,
	"option": true, "options": true, "team": true, "execute": true,
}

// sameAsk is the overlap coefficient of the two word sets against ownerAskOverlap.
func sameAsk(a, b map[string]bool) bool {
	small, large := a, b
	if len(b) < len(a) {
		small, large = b, a
	}
	if len(small) < ownerAskMinWords {
		return false
	}
	shared := 0
	for word := range small {
		if large[word] {
			shared++
		}
	}
	return float64(shared)/float64(len(small)) >= ownerAskOverlap
}
