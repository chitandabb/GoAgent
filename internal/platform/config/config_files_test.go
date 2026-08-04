package config

import (
	"path/filepath"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestRepositoryConfigFilesDecodeAndValidate(t *testing.T) {
	configDirectory := filepath.Join("..", "..", "..", "config")
	for _, name := range []string{"mesguard.toml", "mesguard.docker.toml"} {
		t.Run(name, func(t *testing.T) {
			var cfg Config
			path := filepath.Join(configDirectory, name)
			if _, err := toml.DecodeFile(path, &cfg); err != nil {
				t.Fatalf("DecodeFile(%q): %v", path, err)
			}
			if err := cfg.Validate(); err != nil {
				t.Fatalf("Validate(%q): %v", path, err)
			}
			if cfg.Models.Judge.Enabled {
				t.Fatalf("%q enables the offline Judge by default", path)
			}
			judge := cfg.Models.Judge
			judge.Enabled = true
			if err := judge.Validate(); err != nil {
				t.Fatalf("configured models.judge in %q is invalid: %v", path, err)
			}
		})
	}
}
