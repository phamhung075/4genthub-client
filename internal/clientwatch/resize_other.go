//go:build !unix

package clientwatch

import "context"

// watchResize has no resize signal to listen to here; the width stays the one read at start.
func watchResize(context.Context) <-chan struct{} { return nil }
