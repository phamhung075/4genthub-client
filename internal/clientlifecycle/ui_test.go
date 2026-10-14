package clientlifecycle

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func stubOpenURL(t *testing.T, fn func(string) error) {
	t.Helper()
	original := openURL
	openURL = fn
	t.Cleanup(func() { openURL = original })
}

func TestUIOpensTheConfiguredURL(t *testing.T) {
	t.Setenv(uiURLEnv, "http://ui.example:9")
	var opened string
	stubOpenURL(t, func(url string) error { opened = url; return nil })

	var stderr bytes.Buffer
	openUIImpl(&stderr)

	if opened != "http://ui.example:9" || stderr.Len() != 0 {
		t.Errorf("opened %q, stderr %q", opened, stderr.String())
	}
}

func TestUIDefaultsToTheDevelopmentFrontend(t *testing.T) {
	t.Setenv(uiURLEnv, "")
	var opened string
	stubOpenURL(t, func(url string) error { opened = url; return nil })

	openUIImpl(&bytes.Buffer{})

	if opened != defaultUI {
		t.Errorf("opened %q, want %q", opened, defaultUI)
	}
}

func TestUIFailureIsALineOfStderr(t *testing.T) {
	t.Setenv(uiURLEnv, "")
	stubOpenURL(t, func(string) error { return errors.New("no opener") })

	var stderr bytes.Buffer
	openUIImpl(&stderr)

	if !strings.Contains(stderr.String(), "could not open the 4genthub UI at "+defaultUI) {
		t.Errorf("stderr = %q", stderr.String())
	}
}
