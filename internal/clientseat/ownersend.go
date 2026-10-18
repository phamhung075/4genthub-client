// `4genteam send --via-owner`: method C of ruling 4.6 (section 4.6 of
// ai_docs/operations/client-features-for-the-dev-team.md), the way the owner sends a message a seat can see
// came from them.
//
// The rule the method exists for: the sender a seat sees comes from the environment of the program that runs
// the send (OPENRIG_SESSION_NAME), and the daemon sets that environment on a node's terminal at start, so a
// command typed in the owner node's terminal is signed `<room>-owner@<room>` by the node itself. The client
// therefore writes the owner's name NOWHERE: it writes the message to a private file, types ONE short command
// into the node carrying only file paths, and reads the result the command wrote to a second file. Only paths
// pass through the node's command line, so no message text lands in its shell history or hits a length limit.
//
// And the ruling's trust rule, which this must not appear to weaken: this gives attribution and an audit
// trail, NOT authentication. Everything running as the owner's user can type into that terminal.
package clientseat

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
)

const (
	// ownerSendTimeout is how long the owner's send waits for the node's own command to write its result. The
	// node runs an interactive shell, so this is a real round trip through it; a slow one is still worth
	// waiting for, because the message may already have gone out.
	ownerSendTimeout = 30 * time.Second
	// ownerSendPoll is how often the result file is looked for while the node writes it.
	ownerSendPoll = 250 * time.Millisecond
)

// ownerSendDeps is what the owner route touches outside its arguments, as fields so the suite needs no rig, no
// terminal and no wait.
type ownerSendDeps struct {
	sendKeys func(session, command string) error
	timeout  time.Duration
	poll     time.Duration
}

// ownerSendDepsFor is the seam the verb reads, so a test sends through a node without tmux and without a wait.
var ownerSendDepsFor = defaultOwnerSendDeps

func defaultOwnerSendDeps() *ownerSendDeps {
	return &ownerSendDeps{sendKeys: sendKeysInto, timeout: ownerSendTimeout, poll: ownerSendPoll}
}

// sendKeysInto types one line into a terminal and presses Enter, which is what makes the node's own shell run
// the command and sign what it sends. The command is passed as ONE argument, so the node's shell - not this
// process - expands what is inside it.
func sendKeysInto(session, command string) error {
	path, err := exec.LookPath("tmux")
	if err != nil {
		return fmt.Errorf("tmux is not on PATH, so nothing can type into %s", session)
	}
	done, err := exec.Command(path, "send-keys", "-t", session, "--", command, "Enter").CombinedOutput()
	if err != nil {
		return fmt.Errorf("tmux send-keys -t %s failed: %s", session, clipForReport(strings.TrimSpace(string(done)), 200))
	}
	return nil
}

// clipForReport shortens a child's output so a refusal stays one readable line. It cuts on a RUNE boundary: a
// byte slice would split a multi-byte character and print a replacement mark in the middle of the evidence.
func clipForReport(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	cut := 0
	for i := range text {
		if i > limit {
			break
		}
		cut = i
	}
	return text[:cut] + "..."
}

// ownerNodeSession is the signed name of the room's owner node, `<room>-owner@<room>`: this client never puts a
// name into a message, it types into the terminal that already carries one.
func ownerNodeSession(rig string) string {
	return rig + "-" + ownerMember + "@" + rig
}

// ownerMember is the name the ruling fixes for the room's owner node (4.6). It is named here rather than read
// from a table because this route is its only reader on this side: the node itself is a room member the
// server's render carries, and a client constant table of members would be the second source of truth that
// `policy` already retired.
const ownerMember = "owner"

// sendViaOwnerNode is method C itself: private message file, one typed command, result read back.
func sendViaOwnerNode(deps *ownerSendDeps, rig, seat, text string, stdout, stderr io.Writer) int {
	node := ownerNodeSession(rig)
	target := rig + "-" + seat + "@" + rig

	directory, err := os.MkdirTemp("", "4genteam-send-")
	if err != nil {
		return report(stderr, &clientcmd.CodedError{
			Message: fmt.Sprintf("no private directory for the message: %v", err), Code: ExitFailed,
		})
	}
	messagePath := filepath.Join(directory, "message")
	resultPath := filepath.Join(directory, "result")
	// The paths are single-quoted inside the typed command, so a path carrying a quote would be expanded by the
	// node's shell differently than intended. A temporary directory practically never has one; refusing is the
	// only honest answer when it does, and it is said plainly rather than sending something else.
	if strings.ContainsRune(directory, '\'') {
		os.RemoveAll(directory)
		return report(stderr, &clientcmd.CodedError{
			Message: fmt.Sprintf("the private directory path %s carries a quote, so it cannot be typed safely", directory),
			Code:    ExitFailed,
		})
	}
	if err := os.WriteFile(messagePath, []byte(text+"\n"), 0o600); err != nil {
		os.RemoveAll(directory)
		return report(stderr, &clientcmd.CodedError{
			Message: fmt.Sprintf("the message could not be written to a private file: %v", err), Code: ExitFailed,
		})
	}
	// THE NODE RUNS THIS CLIENT'S OWN SEND, not the predecessor's `rig`: the closed port retired that program, and a
	// command typed into a terminal that would fail is worse than no route at all. It is the same verb a seat
	// uses, run from the node's own shell, which is what makes the node's environment the signature.
	command := fmt.Sprintf("4genteam send %s \"$(cat '%s')\" > '%s' 2>&1", target, messagePath, resultPath)
	if err := deps.sendKeys(node, command); err != nil {
		os.RemoveAll(directory)
		return report(stderr, &clientcmd.CodedError{
			Message: fmt.Sprintf("the message was not sent: %v", err), Code: clientcmd.ExitUnavailable,
		})
	}
	result, answered := waitForOwnerResult(deps, resultPath)
	if !answered {
		// The node may still be running the command, so its files are LEFT WHERE THEY ARE: removing the message
		// file could send an empty message, and removing the result would throw away the only copy. The line
		// names where that result will land, and never repeats the message text.
		return report(stderr, &clientcmd.CodedError{
			Message: fmt.Sprintf("%s did not answer within %s; the result will be written to %s",
				node, deps.timeout, resultPath),
			Code: ExitFailed,
		})
	}
	os.RemoveAll(directory)
	fmt.Fprint(stdout, result)
	return clientcmd.ExitOK
}

// waitForOwnerResult waits for the result file, which the node's own command writes when it is done.
// The file IS the answer, including when it is empty: a command that wrote nothing still ran.
func waitForOwnerResult(deps *ownerSendDeps, path string) (string, bool) {
	deadline := time.Now().Add(deps.timeout)
	for {
		if content, err := os.ReadFile(path); err == nil {
			return string(content), true
		}
		if time.Now().After(deadline) {
			return "", false
		}
		time.Sleep(deps.poll)
	}
}
