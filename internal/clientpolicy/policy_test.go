package clientpolicy

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
)

// writeTeam is the file the roles now come from: the rig's own definition, not a compiled table.
func writeTeam(t *testing.T) string {
	t.Helper()
	body := `{"room":{"slug":"my-rig"},"seats":[` +
		`{"seat_key":"lead","seat_type":"lead"},` +
		`{"seat_key":"go-dev","seat_type":"developer"},` +
		`{"seat_key":"reviewer","seat_type":"reviewer"},` +
		`{"seat_key":"writer","seat_type":"writer","thinking":"medium"}]}`
	path := filepath.Join(t.TempDir(), "team.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func testTeam(t *testing.T) Team {
	t.Helper()
	team, err := ReadTeam(writeTeam(t))
	if err != nil {
		t.Fatal(err)
	}
	return team
}

func parsed(t *testing.T, seat Seat) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(RenderConfig(seat)), &doc); err != nil {
		t.Fatalf("the rendered config is not valid YAML: %v", err)
	}
	return doc
}

// denyPatterns is the bash rules that DENY; the startup exemption is `allow` and is asserted on its own.
func denyPatterns(t *testing.T, doc map[string]any) []string {
	t.Helper()
	bash, _ := doc["bash"].(map[string]any)
	patterns, _ := bash["patterns"].([]any)
	var denied []string
	for _, entry := range patterns {
		rule, _ := entry.(map[string]any)
		if rule["approval"] == "deny" {
			match, _ := rule["match"].(string)
			denied = append(denied, match)
		}
	}
	return denied
}

func toolsDenied(t *testing.T, doc map[string]any) []string {
	t.Helper()
	tools, _ := doc["tools"].(map[string]any)
	approval, _ := tools["approval"].(map[string]any)
	var denied []string
	for tool := range approval {
		denied = append(denied, tool)
	}
	sort.Strings(denied)
	return denied
}

func TestEverySeatExemptsTheStartupRigWhoamiAndOnlyThat(t *testing.T) {
	team := testTeam(t)
	for _, seat := range team.Seats {
		doc := parsed(t, seat)
		bash, _ := doc["bash"].(map[string]any)
		patterns, _ := bash["patterns"].([]any)
		var allowed []string
		for _, entry := range patterns {
			rule, _ := entry.(map[string]any)
			if rule["approval"] == "allow" {
				match, _ := rule["match"].(string)
				allowed = append(allowed, match)
			}
		}
		if len(allowed) != 1 || allowed[0] != "rig whoami*" {
			t.Errorf("%s: allowed patterns are %v, want exactly [rig whoami*]", seat.Key, allowed)
		}
		for _, deny := range denyPatterns(t, doc) {
			if deny == "rig whoami*" {
				t.Errorf("%s: the startup exemption is also denied", seat.Key)
			}
		}
	}
}

func TestEverySeatWaitsForMCPAndChecksCompoundCommands(t *testing.T) {
	for _, seat := range testTeam(t).Seats {
		doc := parsed(t, seat)
		mcp, _ := doc["mcp"].(map[string]any)
		if mcp["startupTimeoutMs"] != 0 {
			t.Errorf("%s: mcp.startupTimeoutMs = %v, want 0", seat.Key, mcp["startupTimeoutMs"])
		}
		bash, _ := doc["bash"].(map[string]any)
		if bash["allowCompoundCommands"] != true {
			t.Errorf("%s: allowCompoundCommands = %v, want true", seat.Key, bash["allowCompoundCommands"])
		}
	}
}

func TestEverySeatIncludingTheLeadIsDeniedPushAmendAndProductionAccess(t *testing.T) {
	for _, seat := range testTeam(t).Seats {
		denied := denyPatterns(t, parsed(t, seat))
		for _, want := range []string{"git push*", "git commit*--amend*", "git add -A*", "git reset --hard*", "ssh *", "tmux kill-*"} {
			found := false
			for _, rule := range denied {
				if rule == want {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("%s is not denied %q", seat.Key, want)
			}
		}
	}
}

func TestOnlyTheLeadKeepsRigLaunchAndTheAgentSeatConnectionTools(t *testing.T) {
	team := testTeam(t)
	for _, seat := range team.Seats {
		doc := parsed(t, seat)
		denied := denyPatterns(t, doc)
		hasRigLaunch := false
		for _, rule := range denied {
			if rule == "rig launch*" {
				hasRigLaunch = true
			}
		}
		if seat.Type == "lead" {
			if hasRigLaunch {
				t.Error("the lead is denied rig launch*")
			}
			if tools := toolsDenied(t, doc); len(tools) != 0 {
				t.Errorf("the lead has tool denies: %v", tools)
			}
			continue
		}
		if !hasRigLaunch {
			t.Errorf("%s keeps rig launch*", seat.Key)
		}
		deniedTools := toolsDenied(t, doc)
		for _, want := range nonLeadToolDeny {
			found := false
			for _, tool := range deniedTools {
				if tool == want {
					found = true
				}
			}
			if !found {
				t.Errorf("%s is not denied %q", seat.Key, want)
			}
		}
	}
}

func TestTheContextSyncToolsAreNeverDenied(t *testing.T) {
	for _, seat := range testTeam(t).Seats {
		denied := toolsDenied(t, parsed(t, seat))
		for _, tool := range []string{"manage_task", "manage_subtask", "manage_context", "manage_project", "manage_git_branch", "call_seat"} {
			for _, entry := range denied {
				if entry == "mcp__agenthub_http_"+tool {
					t.Errorf("%s is denied the context-sync tool %s", seat.Key, tool)
				}
			}
		}
		for _, entry := range denied {
			if strings.Contains(entry, "deepseek") {
				t.Errorf("%s denies a provider token tool: %s", seat.Key, entry)
			}
		}
	}
}

func TestOnlyTheReviewerLosesTheEditTools(t *testing.T) {
	for _, seat := range testTeam(t).Seats {
		denied := toolsDenied(t, parsed(t, seat))
		hasEdit, hasAST := false, false
		for _, entry := range denied {
			if entry == "edit" {
				hasEdit = true
			}
			if entry == "ast_edit" {
				hasAST = true
			}
		}
		want := seat.Type == "reviewer"
		if hasEdit != want || hasAST != want {
			t.Errorf("%s: edit=%v ast_edit=%v, want both %v", seat.Key, hasEdit, hasAST, want)
		}
	}
}

func TestEverySeatKeepsOnlyTheRecentTailVerbatimAtACompaction(t *testing.T) {
	for _, seat := range testTeam(t).Seats {
		compaction, _ := parsed(t, seat)["compaction"].(map[string]any)
		if compaction["keepRecentTokens"] != keepRecentTokens {
			t.Errorf("%s: compaction = %v, want keepRecentTokens %d", seat.Key, compaction, keepRecentTokens)
		}
	}
}

func TestOnlyTheSeatWithAThinkingLevelGetsOne(t *testing.T) {
	for _, seat := range testTeam(t).Seats {
		level := parsed(t, seat)["defaultThinkingLevel"]
		if seat.Thinking == "" && level != nil {
			t.Errorf("%s carries a thinking level it did not declare: %v", seat.Key, level)
		}
		if seat.Thinking != "" && level != seat.Thinking {
			t.Errorf("%s: level = %v, want %q", seat.Key, level, seat.Thinking)
		}
	}
}

func TestAnUnlistedRigOrSeatHasNoPermissiveDefault(t *testing.T) {
	team := testTeam(t)
	if _, err := RoleFor(team, "unknown-rig", "lead"); err == nil {
		t.Error("an unknown rig was accepted")
	}
	var out, errOut bytes.Buffer
	code := runShow([]string{"nobody", "--rig", "my-rig", "--team", writeTeam(t)}, &out, &errOut)
	if code != clientcmd.ExitUsage {
		t.Errorf("an unlisted seat exited %d, want %d (stderr: %q)", code, clientcmd.ExitUsage, errOut.String())
	}
}

func seedState(t *testing.T, root string, team Team) {
	t.Helper()
	for _, seat := range team.Seats {
		if err := os.MkdirAll(filepath.Dir(ConfigPath(root, team.Room.Slug, seat.Key)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func TestApplyWritesEverySeatIsIdempotentAndCheckReportsDrift(t *testing.T) {
	team := testTeam(t)
	root := t.TempDir()
	seedState(t, root, team)
	teamPath := writeTeam(t)
	args := []string{"--rig", "my-rig", "--team", teamPath, "--state-root", root}

	var out, errOut bytes.Buffer
	if code := runApply(append(append([]string{}, args...), "--check"), &out, &errOut); code != ExitDrift {
		t.Errorf("--check on a fresh state root = %d, want %d", code, ExitDrift)
	}
	if _, err := os.Stat(ConfigPath(root, "my-rig", "lead")); !os.IsNotExist(err) {
		t.Error("--check wrote a file")
	}

	out.Reset()
	if code := runApply(args, &out, &errOut); code != clientcmd.ExitOK {
		t.Fatalf("apply = %d (stderr %q)", code, errOut.String())
	}
	for _, seat := range team.Seats {
		got, err := os.ReadFile(ConfigPath(root, "my-rig", seat.Key))
		if err != nil {
			t.Fatalf("%s was not written: %v", seat.Key, err)
		}
		if string(got) != RenderConfig(seat) {
			t.Errorf("%s: the written file is not what this binary renders", seat.Key)
		}
	}

	out.Reset()
	if code := runApply(append(append([]string{}, args...), "--check"), &out, &errOut); code != clientcmd.ExitOK {
		t.Errorf("--check after a write = %d, want %d", code, clientcmd.ExitOK)
	}

	writer := ConfigPath(root, "my-rig", "writer")
	if err := os.WriteFile(writer, []byte("tools: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := runApply(append(append([]string{}, args...), "--check"), &out, &errOut); code != ExitDrift {
		t.Errorf("--check on a broken rule = %d, want %d", code, ExitDrift)
	}
}

func TestApplyRefusesASeatThatWasNeverLaunched(t *testing.T) {
	var out, errOut bytes.Buffer
	code := runApply([]string{"--rig", "my-rig", "--team", writeTeam(t), "--state-root", t.TempDir()}, &out, &errOut)
	if code != clientcmd.ExitUsage {
		t.Errorf("apply on an unseeded state root = %d, want %d", code, clientcmd.ExitUsage)
	}
	if !strings.Contains(errOut.String(), "never launched") {
		t.Errorf("the refusal did not name the reason: %q", errOut.String())
	}
}

func TestCheckToleratesKeysThePolicyDoesNotDefineAndStillCatchesABrokenRule(t *testing.T) {
	team := testTeam(t)
	root := t.TempDir()
	seedState(t, root, team)
	teamPath := writeTeam(t)
	args := []string{"--rig", "my-rig", "--team", teamPath, "--state-root", root}

	var out, errOut bytes.Buffer
	if code := runApply(args, &out, &errOut); code != clientcmd.ExitOK {
		t.Fatalf("apply = %d", code)
	}
	path := ConfigPath(root, "my-rig", "go-dev")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(body, []byte("\nmodel: something-else\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := runApply(append(append([]string{}, args...), "--check"), &out, &errOut); code != clientcmd.ExitOK {
		t.Errorf("--check rejected a key the policy does not define: %d (%q)", code, out.String())
	}

	broken := strings.Replace(string(body), `"git push*"`, `"git pull*"`, 1)
	if err := os.WriteFile(path, []byte(broken), 0o644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := runApply(append(append([]string{}, args...), "--check"), &out, &errOut); code != ExitDrift {
		t.Errorf("--check missed a changed rule: %d", code)
	}
}

func TestShowPrintsTheSeatsConfigAndRefusesAnUnknownRig(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := runShow([]string{"writer", "--rig", "my-rig", "--team", writeTeam(t)}, &out, &errOut); code != clientcmd.ExitOK {
		t.Fatalf("show = %d (stderr %q)", code, errOut.String())
	}
	if !strings.Contains(out.String(), "defaultThinkingLevel: medium") {
		t.Errorf("show did not render the seat's thinking level: %q", out.String())
	}

	out.Reset()
	errOut.Reset()
	if code := runShow([]string{"lead", "--rig", "other-rig", "--team", writeTeam(t)}, &out, &errOut); code != clientcmd.ExitUsage {
		t.Errorf("show for an unknown rig = %d, want %d", code, clientcmd.ExitUsage)
	}
	if !strings.Contains(errOut.String(), "no policy table for rig") {
		t.Errorf("the refusal did not name the rule: %q", errOut.String())
	}
}
