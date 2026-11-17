package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAPIIsOffByDefaultAndReadsEnv(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(Example), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.API.Listen != "" || c.API.Token != "" {
		t.Fatalf("api on by default: %+v", c.API)
	}
	t.Setenv("TRANSARC_API_LISTEN", ":8790")
	t.Setenv("TRANSARC_API_TOKEN", "s3cret")
	c, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.API.Listen != ":8790" || c.API.Token != "s3cret" {
		t.Fatalf("env not applied: %+v", c.API)
	}
}

func TestAPIListenIsValidated(t *testing.T) {
	for _, listen := range []string{":8790", "127.0.0.1:8790", "[::1]:8790"} {
		c := Config{API: API{Listen: listen}}
		c.Defaults()
		c.QSV.GlobalQuality, c.SVT.CRF, c.SVT.Preset, c.SVT.Jobs = 23, 32, 8, 1
		if err := c.Validate(); err != nil {
			t.Fatalf("%s: %v", listen, err)
		}
	}
	for _, listen := range []string{"8790", "localhost", "10.0.0.5:"} {
		c := Config{API: API{Listen: listen}}
		c.Defaults()
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "api.listen") {
			t.Fatalf("%s accepted: %v", listen, err)
		}
	}
}

func TestExampleFileMatchesBuiltIn(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "config.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	file := strings.ReplaceAll(string(b), "\r\n", "\n")
	if file != strings.ReplaceAll(Example, "\r\n", "\n") {
		t.Fatal("config.example.yaml and config.Example differ")
	}
}
