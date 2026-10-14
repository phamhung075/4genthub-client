package clientruntime

import "testing"

// TestNoCompatibilityShimVerbIsRegistered pins the removal of `rig-shim`: the client no longer installs a
// `rig` symlink to itself, because nothing but the retired OpenRig CLI was called `rig`.
func TestNoCompatibilityShimVerbIsRegistered(t *testing.T) {
	for _, cmd := range Commands() {
		if cmd.Name() == "rig-shim" {
			t.Fatalf("rig-shim is registered again: %s", cmd.Summary())
		}
	}
}
