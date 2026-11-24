package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"piped/internal/secrets"
)

func configurationFixture(t *testing.T, settings string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".env")
	if err := secrets.GenerateEnv(path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	keyLine := strings.SplitN(string(data), "\n", 2)[0]
	if err := os.WriteFile(path, []byte(keyLine+"\n"+settings), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestServerUsesEnvironmentSettings(t *testing.T) {
	path := configurationFixture(t, "PIPED_DATABASE_PATH=private/custom.db\n"+
		"PIPED_LISTEN=0.0.0.0:9090\n"+
		"PIPED_PUBLIC_URL=https://piped.example\n"+
		"PIPED_TRUSTED_PROXIES=192.0.2.5\n"+
		"PIPED_LOG_RETENTION_DAYS=30\n")
	configuration, err := loadConfiguration(cliOptions{command: "serve", envFile: path})
	if err != nil {
		t.Fatal(err)
	}
	if configuration.DatabasePath != "private/custom.db" || configuration.ListenAddress != "0.0.0.0:9090" ||
		configuration.PublicURL != "https://piped.example" || configuration.LogRetentionDays != 30 || configuration.Vault == nil {
		t.Fatal("server did not load the deployment settings")
	}
	if len(configuration.TrustedProxies) != 1 || configuration.TrustedProxies[0].String() != "192.0.2.5/32" {
		t.Fatal("server did not load the trusted proxies")
	}
}

func TestInvalidEnvironmentPreventsDatabaseCreation(t *testing.T) {
	for _, setting := range []string{
		"PIPED_LISTEN=0.0.0.0:8080\nPIPED_PUBLIC_URL=http://127.0.0.1:8080\n",
		"PIPED_LISTEN=not-an-address\n",
		"PIPED_PUBLIC_URL=https://piped.example/path\n",
		"PIPED_PUBLIC_URL=https://name:secret@piped.example\n",
		"PIPED_DEVELOPMENT=true\n",
		"PIPED_TRUSTED_PROXIES=secret\n",
	} {
		t.Run(strings.SplitN(setting, "=", 2)[0], func(t *testing.T) {
			t.Chdir(t.TempDir())
			path := configurationFixture(t, setting)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(".env", data, 0600); err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			if err := run(nil, strings.NewReader(""), &output); err == nil {
				t.Fatal("server accepted invalid deployment settings")
			}
			if _, err := os.Stat("data"); !os.IsNotExist(err) {
				t.Fatal("invalid configuration created database files")
			}
			if strings.Contains(output.String(), "secret") {
				t.Fatal("configuration error exposed a value")
			}
		})
	}
}
