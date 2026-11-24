package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDatabaseChecksPreservePreexistingImages(t *testing.T) {
	for _, existing := range []string{"yes", "no", "unavailable"} {
		t.Run(existing, func(t *testing.T) {
			directory := t.TempDir()
			log := filepath.Join(directory, "docker.log")
			docker := `#!/bin/sh
printf '%s\n' "$*" >> "$PIPED_FAKE_DOCKER_LOG"
case "$1 $2" in
  'image ls')
    case "$PIPED_FAKE_IMAGE_EXISTS" in
      yes) printf '%s\n' 'fixture-image' ;;
      unavailable) exit 1 ;;
    esac
    ;;
  'image inspect')
    printf '%s\n' 'mysql@sha256:fixture'
    ;;
  'port '*) printf '%s\n' '127.0.0.1:35333' ;;
  'exec '*) printf '%s\n' 'fixture server' ;;
esac
`
			for name, body := range map[string]string{"docker": docker, "go": "#!/bin/sh\nexit 0\n"} {
				if err := os.WriteFile(filepath.Join(directory, name), []byte(body), 0700); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("PIPED_FAKE_DOCKER_LOG", log)
			t.Setenv("PIPED_FAKE_IMAGE_EXISTS", existing)
			checked, err := checkImage(context.Background(), "mysql:fixture", directory, time.Second)
			if existing == "unavailable" && err == nil {
				t.Fatal("failed Docker inventory was ignored")
			}
			if existing != "unavailable" && (err != nil || checked.Result != "passed") {
				t.Fatalf("fixture check failed: %v", err)
			}
			data, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			if existing == "unavailable" {
				if strings.Contains(string(data), "pull") || strings.Contains(string(data), "rm ") {
					t.Fatal("failed inventory check allowed Docker mutations")
				}
				return
			}
			if !strings.Contains(string(data), "rm --force --volumes piped-database-check-") {
				t.Fatal("verification container was not removed")
			}
			removedImage := strings.Contains(string(data), "image rm mysql:fixture")
			if removedImage != (existing == "no") {
				t.Fatal("cleanup did not preserve preexisting images or remove a new fixture image")
			}
		})
	}
}
