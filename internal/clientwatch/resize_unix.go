//go:build unix

package clientwatch

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

// watchResize delivers one value per SIGWINCH, the signal the terminal sends when its size changes - a zoom
// changes the column count the same way a drag does.
func watchResize(ctx context.Context) <-chan struct{} {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGWINCH)
	resized := make(chan struct{}, 1)
	go func() {
		defer signal.Stop(signals)
		for {
			select {
			case <-ctx.Done():
				return
			case <-signals:
				select {
				case resized <- struct{}{}:
				default:
				}
			}
		}
	}()
	return resized
}
