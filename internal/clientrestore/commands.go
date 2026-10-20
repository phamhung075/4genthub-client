package clientrestore

import "github.com/phamhung075/4genthub-client/internal/clientcmd"

// Commands is what the dispatcher registers from this package: the two verbs that put a stopped room back.
// They live in one package because the watchdog's action IS `continue`'s restore - the shell pair had the
// watchdog call the continue SCRIPT, and here it calls the same function, so the two cannot drift apart.
func Commands() []clientcmd.Command {
	return []clientcmd.Command{continueCommand{}, watchdogCommand{}}
}
