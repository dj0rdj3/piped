package secrets

import (
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestEnvExampleMatchesGeneratedAndRuntimeDefaults(t *testing.T) {
	assignments := func(path string) map[string]string {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		settings := make(map[string]string)
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			name, value, ok := strings.Cut(line, "=")
			if !ok || name == "" {
				t.Fatal("configuration reference contains an invalid assignment")
			}
			if _, exists := settings[name]; exists {
				t.Fatalf("configuration reference repeats %s", name)
			}
			settings[name] = value
		}
		return settings
	}

	example := assignments(filepath.Join("..", "..", ".env.example"))
	if value, exists := example[envKey]; !exists || value != "" {
		t.Fatal(".env.example must include an empty encryption-key placeholder")
	}
	path := filepath.Join(t.TempDir(), ".env")
	if err := GenerateEnv(path); err != nil {
		t.Fatal(err)
	}
	generated := assignments(path)
	generated[envKey] = ""
	if !reflect.DeepEqual(example, generated) {
		t.Fatal("update .env.example to match all generated settings and defaults")
	}

	configuration, err := LoadConfiguration(path)
	if err != nil {
		t.Fatal(err)
	}
	defaults, err := LoadSettings(filepath.Join(t.TempDir(), "missing.env"))
	if err != nil {
		t.Fatal(err)
	}
	configuration.Vault = nil
	if !reflect.DeepEqual(configuration, defaults) {
		t.Fatal("update generated configuration and .env.example to match runtime defaults")
	}
}

func TestSettingsDefaultsAndOptionalSetupKey(t *testing.T) {
	t.Setenv("PIPED_LISTEN", "0.0.0.0:9999")
	t.Setenv("PIPED_PUBLIC_URL", "http://untrusted.example")
	t.Setenv("PIPED_DATABASE_PATH", "other.db")
	t.Setenv("PIPED_TRUSTED_PROXIES", "0.0.0.0/0")
	path := filepath.Join(t.TempDir(), ".env")
	configuration, err := LoadSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	if configuration.DatabasePath != "data/piped.db" || configuration.ListenAddress != "127.0.0.1:8080" ||
		configuration.PublicURL != "http://127.0.0.1:8080" || configuration.LogRetentionDays != 7 || configuration.Vault != nil {
		t.Fatal("unexpected settings defaults or process-environment override")
	}
	if len(configuration.TrustedProxies) != 1 || configuration.TrustedProxies[0].String() != "127.0.0.0/8" {
		t.Fatal("unexpected trusted-proxy defaults or process-environment override")
	}
	if err := os.WriteFile(path, []byte("PIPED_DATABASE_PATH='private/custom.db'\nPIPED_LISTEN=\nPIPED_PUBLIC_URL=\n"), 0600); err != nil {
		t.Fatal(err)
	}
	configuration, err = LoadSettings(path)
	if err != nil || configuration.DatabasePath != "private/custom.db" || configuration.Vault != nil {
		t.Fatal("setup did not accept settings without a key")
	}
	if _, err := LoadConfiguration(path); err == nil {
		t.Fatal("server accepted settings without a deployment key")
	}
}

func TestGeneratedConfigurationIncludesRuntimeDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := GenerateEnv(path); err != nil {
		t.Fatal(err)
	}
	configuration, err := LoadConfiguration(path)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, setting := range []string{
		"PIPED_DATABASE_PATH=" + configuration.DatabasePath,
		"PIPED_LISTEN=" + configuration.ListenAddress,
		"PIPED_PUBLIC_URL=" + configuration.PublicURL,
		"PIPED_TRUSTED_PROXIES=127.0.0.0/8",
		"PIPED_LOG_RETENTION_DAYS=7",
	} {
		if !strings.Contains(string(data), setting+"\n") {
			t.Fatalf("generated configuration is missing %s", strings.SplitN(setting, "=", 2)[0])
		}
	}
}

func TestTrustedProxySettings(t *testing.T) {
	for _, fixture := range []struct {
		name, value string
		want        []netip.Prefix
	}{
		{name: "disabled", value: ""},
		{name: "IPv4 network", value: "127.0.0.0/8", want: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}},
		{name: "mixed addresses and networks", value: " 192.0.2.5, ::1, 2001:db8::1/32 ", want: []netip.Prefix{
			netip.MustParsePrefix("192.0.2.5/32"), netip.MustParsePrefix("::1/128"), netip.MustParsePrefix("2001:db8::/32"),
		}},
		{name: "IPv4 mapped", value: "::ffff:127.0.0.1,::ffff:192.0.2.0/120", want: []netip.Prefix{
			netip.MustParsePrefix("127.0.0.1/32"), netip.MustParsePrefix("192.0.2.0/24"),
		}},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), ".env")
			if err := os.WriteFile(path, []byte("PIPED_TRUSTED_PROXIES="+fixture.value+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			configuration, err := LoadSettings(path)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(configuration.TrustedProxies, fixture.want) {
				t.Fatalf("trusted proxies = %v, want %v", configuration.TrustedProxies, fixture.want)
			}
		})
	}
	for _, value := range []string{"secret", "localhost", "127.0.0.1:8080", "192.0.2.1/33", "::1/129", "fe80::1%eth0", "::ffff:127.0.0.1/80", ",", "127.0.0.1,"} {
		path := filepath.Join(t.TempDir(), ".env")
		if err := os.WriteFile(path, []byte("PIPED_TRUSTED_PROXIES="+value+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		_, err := LoadSettings(path)
		if err == nil {
			t.Fatalf("accepted invalid trusted proxy %q", value)
		}
		if strings.Contains(err.Error(), value) {
			t.Fatal("trusted proxy configuration error exposed its value")
		}
	}
}

func TestSettingsRejectDuplicatesAndUnsupportedAssignments(t *testing.T) {
	for _, body := range []string{
		"PIPED_LISTEN=127.0.0.1:8080\nPIPED_LISTEN=127.0.0.1:9090\n",
		"PIPED_PUBLIC_URL=https://one.example\nPIPED_PUBLIC_URL=https://two.example\n",
		"PIPED_DATABASE_PATH=one.db\nPIPED_DATABASE_PATH=two.db\n",
		"PIPED_DATABASE_PATH=private/secret\x00.db\n",
		"PIPED_DATABASE_PATH=private/secret\r.db\n",
		"PIPED_DEVELOPMENT=secret\n",
		"export PIPED_LISTEN=secret\n",
		"secret\n",
	} {
		path := filepath.Join(t.TempDir(), ".env")
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		_, err := LoadSettings(path)
		if err == nil {
			t.Fatal("invalid settings accepted")
		}
		if strings.Contains(err.Error(), "secret") {
			t.Fatal("configuration error exposed a value")
		}
	}
}

func TestDatabaseDriverSettings(t *testing.T) {
	for _, driver := range []string{"postgres", "mariadb", "mysql"} {
		path := filepath.Join(t.TempDir(), ".env")
		body := "PIPED_DATABASE_DRIVER=" + driver + "\nPIPED_DATABASE_DSN='private-database-connection'\n"
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		settings, err := LoadSettings(path)
		if err != nil || settings.DatabaseDriver != driver || settings.DatabaseDSN != "private-database-connection" {
			t.Fatal("database settings were not loaded")
		}
	}
	for _, body := range []string{
		"PIPED_DATABASE_DRIVER=secret\n",
		"PIPED_DATABASE_DRIVER=postgres\n",
		"PIPED_DATABASE_DRIVER=mysql\nPIPED_DATABASE_DSN=\n",
		"PIPED_DATABASE_DSN=secret\n",
	} {
		path := filepath.Join(t.TempDir(), ".env")
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadSettings(path); err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatal("invalid database settings were accepted or exposed")
		}
	}
}

func TestConfigurationByteBoundaryAndLineIteration(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := GenerateEnv(path); err != nil {
		t.Fatal(err)
	}
	generated, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	keyLine := strings.SplitN(string(generated), "\n", 2)[0]
	for _, ending := range []string{"", "\n", "\r\n"} {
		body := "# comment\r\n\n" + keyLine + "\r\nPIPED_DATABASE_PATH='private/path with spaces.db'" + ending
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		configuration, err := LoadConfiguration(path)
		if err != nil || configuration.DatabasePath != "private/path with spaces.db" || configuration.Vault == nil {
			t.Fatalf("line ending changed configuration parsing: %v", err)
		}
	}
	for _, size := range []int{4096, 4097} {
		body := keyLine + "\n#" + strings.Repeat("x", size-len(keyLine)-2)
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		_, err := LoadConfiguration(path)
		if (err == nil) != (size == 4096) {
			t.Fatalf("configuration size bound changed at %d: %v", size, err)
		}
	}
}
