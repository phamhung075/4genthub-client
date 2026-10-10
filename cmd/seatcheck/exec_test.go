package main

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// Environment variables the fake rig reads. Tests set them with t.Setenv so the script
// writes its observations somewhere the test can inspect.
const (
	fakeRigArgsVar  = "FAKE_RIG_ARGS"
	fakeRigStdinVar = "FAKE_RIG_STDIN"
)

// fakeRig writes an executable /bin/sh script named rig into a fresh temp dir and puts that
// dir first on PATH for the duration of the test. A test that needs no rig at all sets PATH
// to an empty temp dir itself.
func fakeRig(t *testing.T, script string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake rig is a /bin/sh script")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "rig"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake rig: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

// fakeRigArgvLines records argv one argument per line. It is only safe while no argument
// contains a newline, which is what TestRigSendArgv passes.
const fakeRigArgvLines = `#!/bin/sh
printf '%s\n' "$@" > "$FAKE_RIG_ARGS"
if [ ! -t 0 ]; then
	cat > "$FAKE_RIG_STDIN" 2>/dev/null || true
fi
`

// fakeRigArgvNul records argv NUL separated, so a newline inside an argument survives, and
// drains stdin. Under rigSend stdin is /dev/null, so cat sees EOF at once; the -t check keeps
// the script from ever blocking on a terminal.
const fakeRigArgvNul = `#!/bin/sh
printf '%s\0' "$@" > "$FAKE_RIG_ARGS"
if [ ! -t 0 ]; then
	cat > "$FAKE_RIG_STDIN" 2>/dev/null || true
fi
`

// readArgvLines returns the argv the fake rig recorded with the line-per-argument script.
func readArgvLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read recorded argv: %v", err)
	}
	s := strings.TrimSuffix(string(data), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// readArgv returns the argv the fake rig recorded with the NUL separated script.
func readArgv(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read recorded argv: %v", err)
	}
	s := strings.TrimSuffix(string(data), "\x00")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\x00")
}

// rigSend puts the message after --, so the recorded argv is the exact command line rig
// receives: target and text are separate, positional arguments.
func TestRigSendArgv(t *testing.T) {
	dir := t.TempDir()
	argsPath := filepath.Join(dir, "args")
	t.Setenv(fakeRigArgsVar, argsPath)
	t.Setenv(fakeRigStdinVar, filepath.Join(dir, "stdin"))
	fakeRig(t, fakeRigArgvLines)

	var stdout, stderr bytes.Buffer
	if code := rigSend("pod-b@r", "--rig=x", &stdout, &stderr); code != 0 {
		t.Fatalf("rigSend = %d, want 0 (stderr %q)", code, stderr.String())
	}
	got := readArgvLines(t, argsPath)
	want := []string{"send", "--", "pod-b@r", "--rig=x"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %q, want %q", got, want)
	}
}

// A message full of shell and option metacharacters must arrive as one argv element and must
// never be evaluated: the script reads it, it does not run it.
func TestRigSendMessageIsOneArgument(t *testing.T) {
	dir := t.TempDir()
	argsPath := filepath.Join(dir, "args")
	t.Setenv(fakeRigArgsVar, argsPath)
	t.Setenv(fakeRigStdinVar, filepath.Join(dir, "stdin"))
	fakeRig(t, fakeRigArgvNul)

	work := t.TempDir()
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(work); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(oldWD); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})

	msg := "spaces and \"double quotes\" and 'single quotes'\nleading dash -x $(touch pwned) `touch pwned` ; touch pwned"

	var stdout, stderr bytes.Buffer
	if code := rigSend("pod-b@r", msg, &stdout, &stderr); code != 0 {
		t.Fatalf("rigSend = %d, want 0 (stderr %q)", code, stderr.String())
	}
	got := readArgv(t, argsPath)
	want := []string{"send", "--", "pod-b@r", msg}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %q, want %q", got, want)
	}
	if _, err := os.Stat(filepath.Join(work, "pwned")); !os.IsNotExist(err) {
		t.Fatalf("message was evaluated: pwned exists (stat err %v)", err)
	}
}

// rigSend leaves cmd.Stdin unset, so the child reads /dev/null rather than the seat's input.
// Putting a pipe holding LEAK on os.Stdin proves the value is not inherited.
func TestRigSendDetachesStdin(t *testing.T) {
	dir := t.TempDir()
	stdinPath := filepath.Join(dir, "stdin")
	t.Setenv(fakeRigArgsVar, filepath.Join(dir, "args"))
	t.Setenv(fakeRigStdinVar, stdinPath)
	fakeRig(t, fakeRigArgvNul)

	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pw.WriteString("LEAK"); err != nil {
		t.Fatal(err)
	}
	if err := pw.Close(); err != nil {
		t.Fatal(err)
	}
	oldStdin := os.Stdin
	os.Stdin = pr
	t.Cleanup(func() {
		os.Stdin = oldStdin
		pr.Close()
	})

	var stdout, stderr bytes.Buffer
	if code := rigSend("pod-b@r", "hi", &stdout, &stderr); code != 0 {
		t.Fatalf("rigSend = %d, want 0 (stderr %q)", code, stderr.String())
	}
	data, err := os.ReadFile(stdinPath)
	if err != nil {
		t.Fatalf("read recorded stdin: %v", err)
	}
	if len(data) != 0 {
		t.Fatalf("child stdin = %q, want empty (the test's stdin leaked into rig)", data)
	}
}

func TestRigSendReturnsExitCodeUnchanged(t *testing.T) {
	t.Run("exit code 7", func(t *testing.T) {
		fakeRig(t, "#!/bin/sh\nexit 7\n")

		var stdout, stderr bytes.Buffer
		if code := rigSend("pod-b@r", "hi", &stdout, &stderr); code != 7 {
			t.Fatalf("rigSend = %d, want 7 (stderr %q)", code, stderr.String())
		}
	})

	t.Run("rig missing from PATH", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("PATH semantics differ on windows")
		}
		t.Setenv("PATH", t.TempDir())

		var stdout, stderr bytes.Buffer
		if code := rigSend("pod-b@r", "hi", &stdout, &stderr); code != 1 {
			t.Fatalf("rigSend = %d, want 1", code)
		}
		if !strings.Contains(stderr.String(), "deliver") {
			t.Fatalf("stderr = %q, want it to mention deliver", stderr.String())
		}
	})
}

func TestRigWhoamiReadsJSON(t *testing.T) {
	dir := t.TempDir()
	argsPath := filepath.Join(dir, "args")
	t.Setenv(fakeRigArgsVar, argsPath)

	const whoamiJSON = `{"identity":{"rigName":"r","memberId":"a"},"peers":[{"logicalId":"p.b","sessionName":"p-b@r"}]}`
	fakeRig(t, "#!/bin/sh\n"+
		"printf '%s\\0' \"$@\" > \"$FAKE_RIG_ARGS\"\n"+
		"printf '%s' '"+whoamiJSON+"'\n")

	got, err := rigWhoami()
	if err != nil {
		t.Fatalf("rigWhoami: %v", err)
	}
	want := identity{Rig: "r", Member: "a", Peers: map[string][]string{"b": {"p-b@r"}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("identity = %+v, want %+v", got, want)
	}
	if argv := readArgv(t, argsPath); !reflect.DeepEqual(argv, []string{"whoami", "--json"}) {
		t.Fatalf("argv = %q, want [whoami --json]", argv)
	}
}

func TestRigWhoamiFailures(t *testing.T) {
	t.Run("non-zero exit", func(t *testing.T) {
		fakeRig(t, "#!/bin/sh\nexit 1\n")

		if _, err := rigWhoami(); err == nil || !strings.Contains(err.Error(), "rig whoami") {
			t.Fatalf("rigWhoami err = %v, want it to mention rig whoami", err)
		}
	})

	t.Run("invalid json", func(t *testing.T) {
		fakeRig(t, "#!/bin/sh\nprintf '%s' 'not json'\n")

		if _, err := rigWhoami(); err == nil || !strings.Contains(err.Error(), "parse rig whoami") {
			t.Fatalf("rigWhoami err = %v, want it to mention parse rig whoami", err)
		}
	})
}
