// Command check-databases runs integration checks on disposable Docker databases.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Include the series at the three-year boundary through the latest stable series.
var defaultImages = []string{
	"postgres:16", "postgres:17", "postgres:18", "postgres:19",
	"mariadb:11.2", "mariadb:11.3", "mariadb:11.4", "mariadb:11.5",
	"mariadb:11.6", "mariadb:11.7", "mariadb:11.8",
	"mariadb:12.0", "mariadb:12.1", "mariadb:12.2", "mariadb:12.3", "mariadb:13.0",
	"mysql:8.2", "mysql:8.3", "mysql:8.4", "mysql:9.0", "mysql:9.1",
	"mysql:9.2", "mysql:9.3", "mysql:9.4", "mysql:9.5", "mysql:9.6",
	"mysql:9.7", "mysql:26.7", "mysql:26.10",
}

type result struct {
	Image   string `json:"image"`
	Version string `json:"version,omitempty"`
	Digest  string `json:"digest,omitempty"`
	Result  string `json:"result"`
	Tests   string `json:"tests,omitempty"`
}

type checkLimits struct {
	memory   string
	cpus     string
	parallel int
	tests    string
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	report := flag.String("report", ".cache/database-checks.json", "JSON report path")
	startup := flag.Duration("startup-timeout", 3*time.Minute, "database startup deadline")
	memory := flag.String("memory", "1g", "per-container memory and tmpfs limit")
	cpus := flag.String("cpus", "2", "per-container CPU limit")
	parallel := flag.Int("parallel", 2, "Go package and test parallelism")
	tests := flag.String("run", "", "optional Go test name pattern for focused regressions")
	flag.Parse()
	if *parallel < 1 || *memory == "" || *cpus == "" {
		return errors.New("resource limits and positive test parallelism are required")
	}
	images := flag.Args()
	if len(images) == 0 {
		images = defaultImages
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	directory, err := os.MkdirTemp("", "piped-databases-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(directory)
	results := []result{}
	failed := false
	for _, image := range images {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		checked, err := checkImage(ctx, image, directory, *startup, checkLimits{memory: *memory, cpus: *cpus, parallel: *parallel, tests: *tests})
		if err != nil {
			fmt.Fprintf(os.Stderr, "FAIL %s: %v\n", image, err)
			checked = result{Image: image, Result: "failed", Tests: *tests}
			failed = true
		}
		results = append(results, checked)
		data, err := json.MarshalIndent(results, "", "  ")
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(*report), 0700); err != nil {
			return err
		}
		if err := os.WriteFile(*report, append(data, '\n'), 0600); err != nil {
			return err
		}
	}
	if failed {
		return errors.New("database compatibility checks failed; see report")
	}
	return nil
}

func execute(ctx context.Context, environment []string, name string, arguments ...string) (string, error) {
	command := exec.CommandContext(ctx, name, arguments...)
	command.Env = environment
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s failed: %w\n%s", name, err, output)
	}
	return strings.TrimSpace(string(output)), nil
}

func randomHex(size int) string {
	data := make([]byte, size)
	_, _ = rand.Read(data)
	return hex.EncodeToString(data)
}

func checkImage(ctx context.Context, image, directory string, startup time.Duration, limits checkLimits) (result, error) {
	driver, _, valid := strings.Cut(image, ":")
	if !valid || (driver != "postgres" && driver != "mysql" && driver != "mariadb") {
		return result{}, errors.New("use official postgres, mysql, or mariadb image tags")
	}
	password := randomHex(24)
	container := "piped-database-check-" + randomHex(6)
	envFile := filepath.Join(directory, "container.env")
	port, dataPath := "3306", "/var/lib/mysql"
	environment := "MYSQL_ROOT_PASSWORD=" + password + "\nMYSQL_ROOT_HOST=%\nMYSQL_DATABASE=piped\n"
	readiness := []string{"mysqladmin", "ping", "--protocol=tcp", "--host=127.0.0.1", "--silent"}
	versionCommand := []string{"mysqld", "--version"}
	if driver == "postgres" {
		port, dataPath = "5432", "/var/lib/postgresql"
		environment = "POSTGRES_USER=piped\nPOSTGRES_PASSWORD=" + password + "\nPOSTGRES_DB=postgres\nPGDATA=/var/lib/postgresql/data/pgdata\n"
		readiness = []string{"pg_isready", "-h", "127.0.0.1", "-U", "piped", "-d", "postgres"}
		versionCommand = []string{"postgres", "--version"}
	} else if driver == "mariadb" {
		readiness[0] = "mariadb-admin"
		versionCommand[0] = "mariadbd"
	}
	if err := os.WriteFile(envFile, []byte(environment), 0600); err != nil {
		return result{}, err
	}
	defer os.Remove(envFile)
	configFile := filepath.Join(directory, "database.json")
	defer os.Remove(configFile)
	fmt.Println("Preparing", image)
	existing, err := execute(ctx, nil, "docker", "image", "ls", "--quiet", image)
	if err != nil {
		return result{}, err
	}
	if _, err := execute(ctx, nil, "docker", "pull", "--quiet", image); err != nil {
		return result{}, err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if _, err := execute(cleanup, nil, "docker", "rm", "--force", "--volumes", container); err != nil {
			fmt.Fprintln(os.Stderr, "Container cleanup failed:", container)
		}
		if existing == "" {
			if _, err := execute(cleanup, nil, "docker", "image", "rm", image); err != nil {
				fmt.Fprintln(os.Stderr, "Image cleanup failed:", image)
			}
		}
	}()
	if _, err := execute(ctx, nil, "docker", "run", "--detach", "--rm", "--name", container,
		"--memory", limits.memory, "--memory-swap", limits.memory, "--cpus", limits.cpus,
		"--env-file", envFile, "--publish", "127.0.0.1::"+port, "--tmpfs", dataPath+":rw,size="+limits.memory, image); err != nil {
		return result{}, err
	}
	mapping, err := execute(ctx, nil, "docker", "port", container, port+"/tcp")
	if err != nil {
		return result{}, err
	}
	_, hostPort, err := net.SplitHostPort(mapping)
	if err != nil {
		return result{}, err
	}
	readyContext, cancel := context.WithTimeout(ctx, startup)
	defer cancel()
	for {
		arguments := append([]string{"exec", container}, readiness...)
		if _, err := execute(readyContext, nil, "docker", arguments...); err == nil {
			break
		}
		select {
		case <-readyContext.Done():
			return result{}, errors.New("database did not become ready")
		case <-time.After(time.Second):
		}
	}
	dsn := "root:" + password + "@tcp(127.0.0.1:" + hostPort + ")/piped"
	if driver == "postgres" {
		dsn = "postgres://piped:" + password + "@127.0.0.1:" + hostPort + "/postgres?sslmode=disable"
	}
	config, err := json.Marshal(struct{ Driver, DSN string }{driver, dsn})
	if err != nil {
		return result{}, err
	}
	if err := os.WriteFile(configFile, config, 0600); err != nil {
		return result{}, err
	}
	clear(config)
	working, err := os.Getwd()
	if err != nil {
		return result{}, err
	}
	// Override inherited values without placing credentials in process arguments.
	testEnvironment := []string{}
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if name != "CGO_ENABLED" && name != "GOPATH" && name != "GOCACHE" && name != "PIPED_TEST_DATABASE_CONFIG" {
			testEnvironment = append(testEnvironment, entry)
		}
	}
	testEnvironment = append(testEnvironment, "CGO_ENABLED=0", "GOPATH="+filepath.Join(working, ".cache/go"),
		"GOCACHE="+filepath.Join(working, ".cache/go-build"), "PIPED_TEST_DATABASE_CONFIG="+configFile)
	for _, profile := range [][]string{nil, {"-tags", "development"}} {
		arguments := append([]string{"test", "-count=1", "-timeout=8m", "-p=" + fmt.Sprint(limits.parallel), "-parallel=" + fmt.Sprint(limits.parallel)}, profile...)
		if limits.tests != "" {
			arguments = append(arguments, "-run", limits.tests)
		}
		arguments = append(arguments, "./internal/...")
		output, err := execute(ctx, testEnvironment, "go", arguments...)
		if err != nil {
			return result{}, err
		}
		fmt.Println(output)
	}
	version, err := execute(ctx, nil, "docker", append([]string{"exec", container}, versionCommand...)...)
	if err != nil {
		return result{}, err
	}
	digest, err := execute(ctx, nil, "docker", "image", "inspect", image, "--format", "{{index .RepoDigests 0}}")
	if err != nil {
		return result{}, err
	}
	fmt.Printf("PASS %s: %s\n", image, version)
	return result{Image: image, Version: version, Digest: digest, Result: "passed", Tests: limits.tests}, nil
}
