package setup_test

import (
	"context"
	"crypto/sha256"
	"debug/buildinfo"
	"debug/elf"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type containerSetupResult struct {
	Image             string `json:"image"`
	Digest            string `json:"digest,omitempty"`
	SetupSHA256       string `json:"setup_sha256"`
	BuildInputsSHA256 string `json:"build_inputs_sha256"`
	Distribution      string `json:"distribution,omitempty"`
	DistributionID    string `json:"distribution_id,omitempty"`
	Release           string `json:"release,omitempty"`
	Architecture      string `json:"architecture,omitempty"`
	EffectiveTools    string `json:"effective_go_node_npm,omitempty"`
	SystemSetup       string `json:"system_setup"`
	ManagedGo         bool   `json:"managed_go"`
	ManagedNode       bool   `json:"managed_node"`
	RepeatSetup       string `json:"repeat_setup,omitempty"`
	MuslBootstrap     string `json:"missing_node_musl_bootstrap,omitempty"`
	FullSetup         string `json:"full_setup"`
	ProductionBuild   string `json:"make_build"`
	CGOEnabled        string `json:"cgo_enabled,omitempty"`
	StaticELF         string `json:"static_elf,omitempty"`
	BinarySmoke       string `json:"binary_help_smoke,omitempty"`
	BinarySHA256      string `json:"binary_sha256,omitempty"`
	BinaryBytes       int64  `json:"binary_bytes,omitempty"`
	BinaryGoVersion   string `json:"binary_go_version,omitempty"`
	ShellCheck        string `json:"shellcheck,omitempty"`
	Error             string `json:"error,omitempty"`
}

// This opt-in test installs real packages only inside disposable Docker containers.
// It never mounts the source checkout, deployment configuration, or application data.
func TestSetupContainers(t *testing.T) {
	if os.Getenv("PIPED_SETUP_CONTAINER_TESTS") != "1" {
		t.Skip("set PIPED_SETUP_CONTAINER_TESTS=1 to test real Docker distributions")
	}
	images := []string{
		"debian:stable-slim",
		"ubuntu:latest",
		"fedora:latest",
		"alpine:latest",
		"archlinux:latest",
		"opensuse/leap:latest",
	}
	if selected := os.Getenv("PIPED_SETUP_TEST_IMAGES"); selected != "" {
		images = strings.Fields(selected)
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	report := os.Getenv("PIPED_SETUP_TEST_REPORT")
	if report == "" {
		report = filepath.Join("test-results", "setup-containers.json")
	}
	if !filepath.IsAbs(report) {
		report = filepath.Join(root, report)
	}
	snapshot := t.TempDir()
	buildHash, err := snapshotBuildInputs(root, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	setupScript, err := os.ReadFile(filepath.Join(snapshot, "scripts/setup.sh"))
	if err != nil {
		t.Fatal(err)
	}
	setupHash := fmt.Sprintf("%x", sha256.Sum256(setupScript))

	results := make([]containerSetupResult, 0, len(images))
	var reportMutex sync.Mutex
	for _, image := range images {
		t.Run(image, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Minute)
			defer cancel()
			result := containerSetupResult{
				Image: image, SetupSHA256: setupHash, BuildInputsSHA256: buildHash,
				SystemSetup: "failed", FullSetup: "not run", ProductionBuild: "not run",
			}
			err := checkSetupContainer(ctx, t, snapshot, &result)
			if err != nil {
				result.Error = err.Error()
				t.Error(err)
			}
			reportMutex.Lock()
			defer reportMutex.Unlock()
			results = append(results, result)
			data, marshalErr := json.MarshalIndent(results, "", "  ")
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			if err := os.MkdirAll(filepath.Dir(report), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(report, append(data, '\n'), 0600); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// Copy a public source allowlist without prebuilt assets, toolchains, or caches.
func snapshotBuildInputs(root, destination string) (string, error) {
	hash := sha256.New()
	for _, name := range []string{"Makefile", "go.mod", "go.sum", "scripts/setup.sh", "scripts/govulncheck.version", "cmd", "internal", "web"} {
		err := filepath.WalkDir(filepath.Join(root, name), func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			base := entry.Name()
			if entry.IsDir() {
				switch base {
				case "node_modules", "dist", ".cache", ".tools", ".git", "data", "bin":
					return filepath.SkipDir
				}
				if strings.HasPrefix(base, ".tooling-check-") {
					return filepath.SkipDir
				}
				return nil
			}
			if base == ".env" || strings.HasPrefix(base, ".env.") ||
				strings.HasSuffix(base, ".db") || strings.HasSuffix(base, ".db-shm") ||
				strings.HasSuffix(base, ".db-wal") || strings.HasSuffix(base, ".log") {
				return nil
			}
			if !entry.Type().IsRegular() {
				return fmt.Errorf("build input must be a regular file: %s", path)
			}
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			contents, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			target := filepath.Join(destination, relative)
			if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
				return err
			}
			if err := os.WriteFile(target, contents, 0600); err != nil {
				return err
			}
			if _, err := fmt.Fprintf(hash, "%d:%s:%d:", len(relative), filepath.ToSlash(relative), len(contents)); err != nil {
				return err
			}
			_, err = hash.Write(contents)
			return err
		})
		if err != nil {
			return "", err
		}
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}

func TestBuildSnapshotCopiesOnlyCleanSource(t *testing.T) {
	root, destination := t.TempDir(), t.TempDir()
	inputs := map[string]string{
		"Makefile": "build:\n", "go.mod": "module test\n", "go.sum": "checksums\n",
		"scripts/govulncheck.version": "v1.10.0\n",
		"scripts/setup.sh":            "#!/bin/sh\n", "cmd/piped/main.go": "package main\n",
		"internal/store/migrations/sqlite/001.sql": "CREATE TABLE example (id INTEGER);\n",
		"web/src/App.vue":                          "<template><main>Example</main></template>\n",
		"web/package.json":                         "{}\n",
	}
	excluded := []string{
		".env", "data/piped.db", ".tools/go/bin/go", ".cache/go-build/cached", "bin/piped",
		"web/.env.production", "web/test.db", "internal/test.db-wal", "web/debug.log",
		"web/node_modules/package/index.js", "web/dist/assets/prebuilt.js",
		"web/.tooling-check-temporary/index.html",
	}
	for _, name := range excluded {
		inputs[name] = "excluded\n"
	}
	for name, contents := range inputs {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	firstHash, err := snapshotBuildInputs(root, destination)
	if err != nil {
		t.Fatal(err)
	}
	for name, contents := range inputs {
		if contents == "excluded\n" {
			if _, err := os.Lstat(filepath.Join(destination, name)); !os.IsNotExist(err) {
				t.Errorf("excluded file copied: %s (%v)", name, err)
			}
			continue
		}
		copied, err := os.ReadFile(filepath.Join(destination, name))
		if err != nil || string(copied) != contents {
			t.Errorf("build input missing or changed: %s (%v)", name, err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "web/.env.production"), []byte("changed private input"), 0600); err != nil {
		t.Fatal(err)
	}
	secondHash, err := snapshotBuildInputs(root, t.TempDir())
	if err != nil || secondHash != firstHash {
		t.Fatalf("excluded data changed the build fingerprint: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "web/src/App.vue"), []byte("<template>Changed</template>"), 0600); err != nil {
		t.Fatal(err)
	}
	changedHash, err := snapshotBuildInputs(root, t.TempDir())
	if err != nil || changedHash == firstHash {
		t.Fatalf("source change did not change the build fingerprint: %v", err)
	}
	if err := os.Symlink(filepath.Join(root, ".env"), filepath.Join(root, "web/src/leak.js")); err != nil {
		t.Fatal(err)
	}
	if _, err := snapshotBuildInputs(root, t.TempDir()); err == nil {
		t.Fatal("build snapshot followed a source symlink")
	}
}

func dockerSetup(ctx context.Context, arguments ...string) (string, error) {
	command := exec.CommandContext(ctx, "docker", arguments...)
	output, err := command.CombinedOutput()
	if err != nil {
		// Package managers can produce very long logs; retain a useful failure tail.
		if len(output) > 6000 {
			output = output[len(output)-6000:]
		}
		return string(output), fmt.Errorf("docker %s: %w\n%s", strings.Join(arguments, " "), err, output)
	}
	return strings.TrimSpace(string(output)), nil
}

func pullSetupImage(ctx context.Context, t *testing.T, image string) (string, error) {
	t.Helper()
	_, existingErr := dockerSetup(ctx, "image", "inspect", image)
	if _, err := dockerSetup(ctx, "pull", "--quiet", image); err != nil {
		return "", err
	}
	if existingErr != nil {
		t.Cleanup(func() {
			cleanup, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			if _, err := dockerSetup(cleanup, "image", "rm", image); err != nil {
				t.Error("image cleanup:", err)
			}
		})
	}
	return dockerSetup(ctx, "image", "inspect", "--format", "{{index .RepoDigests 0}}", image)
}

func cleanupSetupContainer(t *testing.T, name string) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if _, err := dockerSetup(ctx, "rm", "--force", "--volumes", name); err != nil {
			t.Error("container cleanup:", err)
		}
	})
}

func checkSetupContainer(ctx context.Context, t *testing.T, snapshot string, result *containerSetupResult) error {
	t.Helper()
	t.Log("Installing real distribution packages in", result.Image)
	digest, err := pullSetupImage(ctx, t, result.Image)
	if err != nil {
		return err
	}
	result.Digest = digest
	name := fmt.Sprintf("piped-setup-check-%d", time.Now().UnixNano())
	if _, err := dockerSetup(ctx, "run", "--detach", "--name", name, "--label", "piped.verification=setup",
		"--cpus", "2", "--memory", "4g", "--pids-limit", "512",
		"--workdir", "/work/piped", result.Image, "sh", "-c", "while :; do sleep 3600; done"); err != nil {
		return err
	}
	cleanupSetupContainer(t, name)
	if _, err := dockerSetup(ctx, "cp", snapshot+"/.", name+":/work/piped"); err != nil {
		return err
	}
	release, err := dockerSetup(ctx, "exec", name, "sh", "-c", ". /etc/os-release; printf '%s\\n%s\\n%s' \"$ID\" \"${VERSION_ID-rolling}\" \"$PRETTY_NAME\"")
	if err != nil {
		return err
	}
	fields := strings.SplitN(release, "\n", 3)
	if len(fields) != 3 {
		return fmt.Errorf("unexpected distribution release information: %q", release)
	}
	result.DistributionID, result.Release, result.Distribution = fields[0], fields[1], fields[2]
	result.Architecture, err = dockerSetup(ctx, "exec", name, "uname", "-m")
	if err != nil {
		return err
	}
	output, setupErr := dockerSetup(ctx, "exec", name, "sh", "scripts/setup.sh")
	if setupErr != nil {
		return setupErr
	}
	if !strings.Contains(output, "Setup complete. Run make build.") {
		return fmt.Errorf("setup did not confirm completion: %s", output)
	}
	result.SystemSetup = "passed"
	result.FullSetup = "passed"
	t.Log("Building production binary in", result.Distribution)
	result.ProductionBuild = "failed"
	if _, err := dockerSetup(ctx, "exec", name, "make", "build"); err != nil {
		return err
	}
	result.ProductionBuild = "passed"
	if err := inspectProductionBinary(ctx, t, name, result); err != nil {
		return err
	}
	output, err = dockerSetup(ctx, "exec", name, "make", "--no-print-directory", "-s", "setup")
	if err != nil {
		return err
	}
	if strings.Contains(output, "Installing go ") || strings.Contains(output, "Installing node ") {
		return fmt.Errorf("repeat setup downloaded an already installed toolchain: %s", output)
	}
	result.RepeatSetup = "passed"
	if result.DistributionID == "alpine" && result.Architecture == "x86_64" {
		target, err := dockerSetup(ctx, "exec", name, "readlink", ".tools/node")
		if err != nil || !strings.Contains(target, "linux-x64-musl") {
			return fmt.Errorf("Alpine did not bootstrap the official musl Node archive: %q (%v)", target, err)
		}
		result.MuslBootstrap = "passed"
	}
	managed, err := dockerSetup(ctx, "exec", name, "sh", "-c", "test ! -L .tools/go || printf 'go '; test ! -L .tools/node || printf 'node'")
	if err != nil {
		return err
	}
	result.ManagedGo = strings.Contains(managed, "go")
	result.ManagedNode = strings.Contains(managed, "node")
	// A fresh make process must find project-installed tools without inherited PATH.
	result.EffectiveTools, err = dockerSetup(ctx, "exec", name, "make", "--no-print-directory", "-s", "--eval",
		"setup-test-versions: ; @go version && node --version && npm --version", "setup-test-versions")
	if err != nil {
		return err
	}
	if _, err := dockerSetup(ctx, "exec", name, "shellcheck", "scripts/setup.sh"); err != nil {
		return err
	}
	result.ShellCheck = "passed"
	t.Log("PASS", result.Distribution, "setup, make build, static CGO-free production binary, repeat setup, ShellCheck")
	return nil
}

func inspectProductionBinary(ctx context.Context, t *testing.T, container string, result *containerSetupResult) error {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "piped")
	if _, err := dockerSetup(ctx, "cp", container+":/work/piped/bin/piped", binary); err != nil {
		return err
	}
	metadata, err := buildinfo.ReadFile(binary)
	if err != nil {
		return err
	}
	result.BinaryGoVersion = metadata.GoVersion
	for _, setting := range metadata.Settings {
		if setting.Key == "CGO_ENABLED" {
			result.CGOEnabled = setting.Value
		}
	}
	if result.CGOEnabled != "0" {
		return fmt.Errorf("production binary has CGO_ENABLED=%q", result.CGOEnabled)
	}
	executable, err := elf.Open(binary)
	if err != nil {
		return err
	}
	defer executable.Close()
	for _, program := range executable.Progs {
		if program.Type == elf.PT_INTERP {
			return fmt.Errorf("production binary contains an ELF interpreter")
		}
	}
	dependencies, err := executable.DynString(elf.DT_NEEDED)
	if err != nil {
		return err
	}
	if len(dependencies) != 0 {
		return fmt.Errorf("production binary needs dynamic libraries: %v", dependencies)
	}
	result.StaticELF = "passed"
	contents, err := os.ReadFile(binary)
	if err != nil {
		return err
	}
	result.BinarySHA256 = fmt.Sprintf("%x", sha256.Sum256(contents))
	result.BinaryBytes = int64(len(contents))
	help, err := dockerSetup(ctx, "exec", container, "./bin/piped", "--help")
	if err != nil {
		return err
	}
	if !strings.Contains(help, "Usage: piped") {
		return fmt.Errorf("production binary did not print its CLI usage: %s", help)
	}
	result.BinarySmoke = "passed"
	return nil
}
