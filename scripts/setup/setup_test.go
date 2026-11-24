package setup_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const mockTool = `#!/bin/sh
set -eu
name=${0##*/}
if [ "$name" = id ]; then
  printf '%s\n' "${MOCK_UID:-0}"
  exit 0
fi
printf '%s' "$name" >> "$MOCK_LOG"
for argument in "$@"; do
  printf ' <%s>' "$argument" >> "$MOCK_LOG"
done
printf '\n' >> "$MOCK_LOG"
if [ "${MOCK_FAIL-}" = "$name $*" ]; then
  exit 19
fi
case "$name" in
  uname)
    if [ "$1" = -s ]; then printf 'Linux\n'; else printf '%s\n' "${MOCK_ARCH:-x86_64}"; fi
    ;;
  ldd) printf '%s\n' "${MOCK_LIBC:-glibc}" ;;
  sudo|doas) exec "$@" ;;
  apt-get|dnf|yum|apk|pacman|zypper)
    if [ "${MOCK_FAIL_PACKAGE:-0}" = 1 ]; then exit 19; fi
    if [ "${MOCK_INSTALL:-1}" = 1 ]; then
      for tool in bash go node npm cc make git ssh curl rg shellcheck; do
        /bin/ln -sf "$MOCK_DRIVER" "$MOCK_BIN/$tool"
      done
    fi
    ;;
  go)
    if [ "${1-}" = version ]; then
      version=${MOCK_GO_VERSION:-1.27.4}
      case "$0" in */.tools/*) version=${MOCK_UNPACKED_GO_VERSION:-1.27.4} ;; esac
      printf 'go version go%s linux/amd64\n' "$version"
    elif [ "$*" = 'mod download' ]; then
      printf 'go cwd=<%s> cgo=<%s> cache=<%s>\n' "$PWD" "$CGO_ENABLED" "$GOCACHE" >> "$MOCK_LOG"
    fi
    ;;
  node)
    if [ "${1-}" = --version ]; then
      version=${MOCK_NODE_VERSION:-24.22.2}
      case "$0" in */.tools/*) version=${MOCK_UNPACKED_NODE_VERSION:-24.22.2} ;; esac
      printf 'v%s\n' "$version"
    else
      exec "$MOCK_NODE" "$@"
    fi
    ;;
  curl)
    if [ "${MOCK_DOWNLOAD_FAIL:-0}" = 1 ]; then printf 'Download failed\n' >&2; exit 19; fi
    while [ "$#" -gt 0 ]; do
      if [ "$1" = --output ]; then shift; output=$1; fi
      url=$1
      shift
    done
    printf '%s\n' "$url" > "$output"
    ;;
  sha256sum)
    if [ "${MOCK_BAD_CHECKSUM:-0}" = 1 ]; then exit 1; fi
    read -r checksum filename
    [ "${#checksum}" = 64 ] && [ -f "$filename" ]
    ;;
  tar)
    while [ "$#" -gt 0 ]; do
      if [ "$1" = -xzf ]; then shift; archive=$1; fi
      if [ "$1" = -C ]; then shift; directory=$1; fi
      shift
    done
    /bin/mkdir -p "$directory/bin"
    read -r url < "$archive"
    case "$url" in
      *node-v*)
        /bin/ln -s "$MOCK_DRIVER" "$directory/bin/node"
        /bin/ln -s "$MOCK_DRIVER" "$directory/bin/npm"
        ;;
      *) /bin/ln -s "$MOCK_DRIVER" "$directory/bin/go" ;;
    esac
    ;;
esac
`

type setupFixture struct {
	root        string
	bin         string
	driver      string
	log         string
	environment []string
}

func newSetupFixture(t *testing.T) *setupFixture {
	t.Helper()
	root := filepath.Join(t.TempDir(), "source tree")
	fixture := &setupFixture{
		root:   root,
		bin:    filepath.Join(root, "mock-bin"),
		driver: filepath.Join(root, "mock-tool"),
		log:    filepath.Join(root, "commands.log"),
	}
	for _, directory := range []string{"mock-bin", "scripts", "web"} {
		if err := os.MkdirAll(filepath.Join(root, directory), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"scripts/setup.sh", "go.mod", "web/package.json"} {
		contents, err := os.ReadFile(filepath.Join("../..", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), contents, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(fixture.driver, []byte(mockTool), 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"dirname", "cat", "env", "sh", "mkdir", "rmdir", "rm", "mv", "ln", "awk", "mktemp", "gzip", "grep"} {
		target, err := exec.LookPath(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(fixture.bin, name)); err != nil {
			t.Fatal(err)
		}
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	fixture.environment = []string{
		"PATH=" + fixture.bin,
		"MOCK_BIN=" + fixture.bin,
		"MOCK_DRIVER=" + fixture.driver,
		"MOCK_LOG=" + fixture.log,
		"MOCK_NODE=" + node,
	}
	fixture.addTools(t, "id", "uname", "ldd", "sha256sum", "tar")
	return fixture
}

func (fixture *setupFixture) addTools(t *testing.T, names ...string) {
	t.Helper()
	for _, name := range names {
		if err := os.Symlink(fixture.driver, filepath.Join(fixture.bin, name)); err != nil {
			t.Fatal(err)
		}
	}
}

func (fixture *setupFixture) run(t *testing.T, script string, arguments ...string) (string, string, error) {
	t.Helper()
	command := exec.Command("/bin/sh", append([]string{filepath.Join(fixture.root, "scripts", script)}, arguments...)...)
	command.Dir = t.TempDir()
	command.Env = fixture.environment
	output, err := command.CombinedOutput()
	log, readErr := os.ReadFile(fixture.log)
	if readErr != nil && !os.IsNotExist(readErr) {
		t.Fatal(readErr)
	}
	return string(output), string(log), err
}

func TestPackageManagers(t *testing.T) {
	for _, testCase := range []struct {
		manager string
		want    []string
	}{
		{"apt-get", []string{"apt-get <update>", "apt-get <install> <-y> <bash> <build-essential>", "<coreutils>", "<openssh-client>"}},
		{"dnf", []string{"dnf <install> <-y> <bash> <gcc> <make>", "<libstdc++>", "<openssh-clients>", "<ShellCheck>"}},
		{"yum", []string{"yum <install> <-y> <bash> <gcc>", "<openssh-clients>", "<ShellCheck>"}},
		{"apk", []string{"apk <add> <--no-cache> <bash> <build-base>", "<coreutils>", "<openssh-client>"}},
		{"pacman", []string{"pacman <-Syu> <--needed> <--noconfirm>", "<gcc> <make>", "<openssh>"}},
		{"zypper", []string{"zypper <--non-interactive> <refresh>", "zypper <--non-interactive> <install>", "<openssh-clients>", "<timezone>", "<ShellCheck>"}},
	} {
		t.Run(testCase.manager, func(t *testing.T) {
			fixture := newSetupFixture(t)
			fixture.addTools(t, testCase.manager)
			output, log, err := fixture.run(t, "setup.sh")
			if err != nil {
				t.Fatalf("setup failed: %v\n%s\n%s", err, output, log)
			}
			for _, want := range append(testCase.want, "<ca-certificates>", "<ripgrep>", "npm <--prefix> <"+fixture.root+"/web> <ci> <--engine-strict>", "go <mod> <download>", "go cwd=<"+fixture.root+"> cgo=<0> cache=<"+fixture.root+"/.cache/go-build>") {
				if !strings.Contains(log, want) {
					t.Errorf("missing %q in command log:\n%s", want, log)
				}
			}
			if strings.Contains(log, "sudo") || strings.Contains(log, "doas") {
				t.Errorf("root setup requested elevation:\n%s", log)
			}
		})
	}
}

func TestPrivilegeCommands(t *testing.T) {
	for _, privilege := range []string{"sudo", "doas"} {
		t.Run(privilege, func(t *testing.T) {
			fixture := newSetupFixture(t)
			fixture.addTools(t, "apk", privilege)
			fixture.environment = append(fixture.environment, "MOCK_UID=1000")
			output, log, err := fixture.run(t, "setup.sh", "--system-only")
			if err != nil || !strings.Contains(log, privilege+" <apk> <add>") {
				t.Fatalf("elevation failed: %v\n%s\n%s", err, output, log)
			}
			if strings.Contains("\n"+log, "\nnpm <") || strings.Contains("\n"+log, "\ngo <") {
				t.Fatalf("system-only setup ran project commands:\n%s", log)
			}
		})
	}
}

func TestSetupFailuresStopBeforeProjectInstallation(t *testing.T) {
	for _, testCase := range []struct {
		name        string
		tools       []string
		environment []string
		arguments   []string
		want        string
		forbidden   string
	}{
		{"unknown manager", nil, nil, nil, "No supported package manager", "npm"},
		{"no privileges", []string{"apk"}, []string{"MOCK_UID=1000"}, nil, "need root, sudo, or doas", "apk <"},
		{"skip missing tools", []string{"apk"}, nil, []string{"--skip-system-packages"}, "Missing development tools", "apk <"},
		{"apt refresh failure", []string{"apt-get"}, []string{"MOCK_FAIL=apt-get update"}, nil, "System package installation failed", "apt-get <install>"},
		{"zypper refresh failure", []string{"zypper"}, []string{"MOCK_FAIL=zypper --non-interactive refresh"}, nil, "System package installation failed", "<install>"},
		{"install failure", []string{"apk"}, []string{"MOCK_FAIL_PACKAGE=1"}, nil, "System package installation failed", "npm <--prefix>"},
		{"tools still missing", []string{"apk"}, []string{"MOCK_INSTALL=0"}, nil, "Missing development tools", "npm <--prefix>"},
		{"invalid option", nil, nil, []string{"--unknown"}, "Usage:", "npm"},
		{"conflicting options", nil, nil, []string{"--system-only", "--skip-system-packages"}, "Usage:", "npm"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newSetupFixture(t)
			fixture.addTools(t, testCase.tools...)
			fixture.environment = append(fixture.environment, testCase.environment...)
			output, log, err := fixture.run(t, "setup.sh", testCase.arguments...)
			if err == nil || !strings.Contains(output, testCase.want) || strings.Contains(log, testCase.forbidden) {
				t.Fatalf("unexpected failure behavior: %v\n%s\n%s", err, output, log)
			}
		})
	}
}

func TestExistingToolsNeedNoPackageManager(t *testing.T) {
	for _, arguments := range [][]string{nil, {"--skip-system-packages"}} {
		fixture := newSetupFixture(t)
		fixture.addTools(t, "bash", "go", "node", "npm", "cc", "make", "git", "ssh", "curl", "rg", "shellcheck")
		fixture.environment = append(fixture.environment, "MOCK_UID=1000")
		output, log, err := fixture.run(t, "setup.sh", arguments...)
		if err != nil || !strings.Contains(output, "Setup complete") || !strings.Contains(log, "go <mod> <download>") {
			t.Fatalf("existing-tool setup failed: %v\n%s\n%s", err, output, log)
		}
	}
}

func TestToolchainVersions(t *testing.T) {
	for _, testCase := range []struct {
		goVersion   string
		nodeVersion string
		installTool string
	}{
		{"1.27.4", "22.18.0", ""},
		{"1.27.4", "22.19.0", ""},
		{"1.27.4", "24.12.0", ""},
		{"1.27.4", "24.22.2", ""},
		{"1.27.4-X:nodwarf5", "22.23.1", ""},
		{"1.27.4", "25.0.0", ""},
		{"2.0.0", "26.0.0", ""},
		{"1.27.3", "24.12.0", "go"},
		{"1.27.3-X:nodwarf5", "24.12.0", "go"},
		{"1.26.9", "24.12.0", "go"},
		{"1.27.4rc1", "24.12.0", "go"},
		{"devel", "24.12.0", "go"},
		{"1.27.4", "20.20.0", "node"},
		{"1.27.4", "22.17.9", "node"},
		{"1.27.4", "23.9.0", "node"},
		{"1.27.4", "24.11.9", "node"},
		{"1.27.4", "24.12.0-rc.1", "node"},
	} {
		t.Run(testCase.goVersion+"/"+testCase.nodeVersion, func(t *testing.T) {
			fixture := newSetupFixture(t)
			fixture.addTools(t, "bash", "go", "node", "npm", "cc", "make", "git", "ssh", "curl", "rg", "shellcheck")
			fixture.environment = append(fixture.environment, "MOCK_GO_VERSION="+testCase.goVersion, "MOCK_NODE_VERSION="+testCase.nodeVersion)
			output, log, err := fixture.run(t, "setup.sh")
			if err != nil || !strings.Contains(output, "Setup complete") {
				t.Fatalf("toolchain setup failed: %v\n%s\n%s", err, output, log)
			}
			if testCase.installTool == "" {
				if strings.Contains("\n"+log, "\ncurl <") {
					t.Fatalf("compatible toolchain was replaced:\n%s", log)
				}
			} else if !strings.Contains(output, "Installing "+testCase.installTool+" ") {
				t.Fatalf("outdated toolchain was not upgraded:\n%s", output)
			}
		})
	}
}

func TestAutomaticToolchainInstallation(t *testing.T) {
	fixture := newSetupFixture(t)
	fixture.addTools(t, "bash", "npm", "cc", "make", "git", "ssh", "curl", "rg", "shellcheck")
	for attempt := range 2 {
		output, log, err := fixture.run(t, "setup.sh", "--skip-system-packages")
		if err != nil {
			t.Fatalf("attempt %d failed: %v\n%s\n%s", attempt, err, output, log)
		}
		if strings.Count("\n"+log, "\ncurl <") != 2 {
			t.Fatalf("toolchains should download only once across repeated setup:\n%s", log)
		}
	}
	for _, name := range []string{"go", "node"} {
		target, err := os.Readlink(filepath.Join(fixture.root, ".tools", name))
		if err != nil || strings.Contains(target, "/") {
			t.Fatalf("invalid managed toolchain link %q: %v", target, err)
		}
	}
	entries, err := os.ReadDir(filepath.Join(fixture.root, ".tools"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".install-") {
			t.Errorf("temporary installation entry left behind: %s", entry.Name())
		}
	}
}

func TestToolchainDownloadFailures(t *testing.T) {
	for _, testCase := range []struct {
		name        string
		environment string
		want        string
		forbidden   string
	}{
		{"checksum mismatch", "MOCK_BAD_CHECKSUM=1", "SHA-256 verification failed", "\ntar <"},
		{"download failure", "MOCK_DOWNLOAD_FAIL=1", "Download failed", "\ntar <"},
		{"wrong archive version", "MOCK_UNPACKED_NODE_VERSION=24.22.20", "Unexpected node version", "\nnpm <--prefix>"},
		{"unsupported architecture", "MOCK_ARCH=riscv64", "supports x86_64 and ARM64", "\ncurl <"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newSetupFixture(t)
			fixture.addTools(t, "bash", "go", "node", "npm", "cc", "make", "git", "ssh", "curl", "rg", "shellcheck")
			fixture.environment = append(fixture.environment, "MOCK_NODE_VERSION=20.0.0", testCase.environment)
			output, log, err := fixture.run(t, "setup.sh", "--skip-system-packages")
			if err == nil || !strings.Contains(output, testCase.want) || strings.Contains("\n"+log, testCase.forbidden) {
				t.Fatalf("unexpected download failure behavior: %v\n%s\n%s", err, output, log)
			}
			if _, err := os.Lstat(filepath.Join(fixture.root, ".tools", "node")); !os.IsNotExist(err) {
				t.Fatalf("failed download activated a toolchain: %v", err)
			}
			entries, err := os.ReadDir(filepath.Join(fixture.root, ".tools"))
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("failed installation left temporary files: %v", entries)
			}
		})
	}
}

func TestToolchainArchiveSelection(t *testing.T) {
	for _, testCase := range []struct {
		architecture string
		libc         string
		artifact     string
	}{
		{"x86_64", "glibc", "node-v24.22.2-linux-x64.tar.gz"},
		{"x86_64", "musl", "node-v24.22.2-linux-x64-musl.tar.gz"},
		{"aarch64", "glibc", "node-v24.22.2-linux-arm64.tar.gz"},
	} {
		t.Run(testCase.architecture+"/"+testCase.libc, func(t *testing.T) {
			fixture := newSetupFixture(t)
			fixture.addTools(t, "bash", "go", "node", "npm", "cc", "make", "git", "ssh", "curl", "rg", "shellcheck")
			fixture.environment = append(fixture.environment, "MOCK_NODE_VERSION=20.0.0", "MOCK_ARCH="+testCase.architecture, "MOCK_LIBC="+testCase.libc)
			output, log, err := fixture.run(t, "setup.sh", "--skip-system-packages")
			if err != nil || !strings.Contains(log, "https://nodejs.org/dist/v24.22.2/"+testCase.artifact) {
				t.Fatalf("incorrect Node archive selection: %v\n%s\n%s", err, output, log)
			}
		})
	}
}

func TestToolchainPinsMatchProject(t *testing.T) {
	script, err := os.ReadFile("../setup.sh")
	if err != nil {
		t.Fatal(err)
	}
	dockerfile, err := os.ReadFile("../../Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"go", "node"} {
		pin := regexp.MustCompile(`(?m)^` + tool + `_release=([0-9.]+)$`).FindSubmatch(script)
		if len(pin) != 2 || !strings.Contains(string(dockerfile), "ARG "+strings.ToUpper(tool)+"_VERSION="+string(pin[1])+"\n") {
			t.Fatalf("%s setup and Dockerfile toolchain pins differ", tool)
		}
	}
}
