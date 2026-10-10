package clientsync

import "github.com/phamhung075/4genthub-client/internal/clientcmd"

// ClientVersion is what every `hello` frame carries (2.2: `client_version`). It is clientcmd.Version and
// not a second literal, because a version string that drifts between what the binary prints and what the
// wire claims is worse than no version at all.
const ClientVersion = clientcmd.Version
