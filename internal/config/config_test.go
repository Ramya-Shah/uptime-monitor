package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoad(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr bool
	}{
		{"defaults", "sites:\n  - {name: a, url: https://a.test}\n", false},
		{"no sites", "sites: []\n", true},
		{"duplicate", "sites:\n  - {name: a, url: https://a.test}\n  - {name: a, url: https://b.test}\n", true},
		{"bad url", "sites:\n  - {name: a, url: not-a-url}\n", true},
		{"timeout >= interval", "sites:\n  - {name: a, url: https://a.test, interval: 5s, timeout: 5s}\n", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "sites.yaml")
			os.WriteFile(p, []byte(tc.yaml), 0o600)
			c, err := Load(p)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if err == nil && (c.Sites[0].Interval != DefaultInterval || c.Sites[0].Timeout != DefaultTimeout) {
				t.Errorf("defaults not applied: %+v", c.Sites[0])
			}
		})
	}
}

func TestLoadRealConfig(t *testing.T) {
	c, err := Load("../../sites.yaml")
	if err != nil {
		t.Fatal(err)
	}
	// The shipped file must parse, and every site must get sane timing.
	for _, s := range c.Sites {
		if s.Interval < 30*time.Second || s.Timeout <= 0 {
			t.Errorf("site %s: interval %v timeout %v", s.Name, s.Interval, s.Timeout)
		}
	}
}
