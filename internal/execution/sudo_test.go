package execution

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestManagedSudoPreservesShellArgumentsAndInput(t *testing.T) {
	cases := []struct{ name, script, input, output string }{
		{"plain", `sudo printf '%s' 'hello world'`, "", "hello world"},
		{"options", `sudo -nEHSk -p 'ignored prompt' -u root -- printf '%s' 'hello world'`, "", "hello world"},
		{"long options", `sudo --non-interactive --stdin --prompt=ignored --preserve-env=LANG --set-home --user=root printf '%s' ok`, "", "ok"},
		{"numeric root", `sudo -u '#0' printf '%s' ok`, "", "ok"},
		{"attached user", `sudo -uroot printf '%s' ok`, "", "ok"},
		{"environment", `sudo PIPED_FIXTURE_VALUE='hello world' printenv PIPED_FIXTURE_VALUE`, "", "hello world\n"},
		{"pipeline", `printf abc | sudo tr a-z A-Z`, "", "ABC"},
		{"stdin", `sudo -S bash -c 'IFS= read -r answer; printf "%s" "$answer"'`, "human input\n", "human input"},
		{"nested bash", `export -f sudo; sudo bash -c 'sudo printf "%s" nested'`, "", "nested"},
		{"literal text", `sudo printf '%s' 'sudo $(touch should-not-exist); "quote"'`, "", `sudo $(touch should-not-exist); "quote"`},
		{"here document", "sudo cat <<'SCRIPT'\nsudo is literal data\nSCRIPT", "", "sudo is literal data\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, "bash", "--noprofile", "--norc", "-c", managedSudo+"\n"+tc.script)
			command.Dir = t.TempDir()
			command.Stdin = strings.NewReader(tc.input)
			output, err := command.CombinedOutput()
			if err != nil || string(output) != tc.output {
				t.Fatalf("output=%q expected=%q error=%v", output, tc.output, err)
			}
		})
	}
}

func TestManagedSudoRejectsUnsupportedModes(t *testing.T) {
	for _, arguments := range []string{"", "-u", "-p", "-u nobody printf should-not-run", "--user=nobody printf should-not-run", "-i printf should-not-run", "-s printf should-not-run", "--unknown printf should-not-run", "-nZ printf should-not-run", "-b printf should-not-run"} {
		t.Run(arguments, func(t *testing.T) {
			command := exec.Command("bash", "--noprofile", "--norc", "-c", managedSudo+"\nsudo "+arguments)
			output, err := command.CombinedOutput()
			if err == nil || command.ProcessState.ExitCode() != 2 || !strings.HasPrefix(string(output), "Piped:") || strings.Contains(string(output), "should-not-run") {
				t.Fatalf("unexpected result %q: %v", output, err)
			}
		})
	}
}
