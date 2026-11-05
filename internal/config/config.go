// Package config loads the list of sites to monitor from a YAML file.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	DefaultInterval = 30 * time.Second
	DefaultTimeout  = 10 * time.Second
)

// Site is one monitored URL.
type Site struct {
	Name     string        `yaml:"name"`
	URL      string        `yaml:"url"`
	Interval time.Duration `yaml:"interval"`
	Timeout  time.Duration `yaml:"timeout"`
}

type Config struct {
	Sites []Site `yaml:"sites"`
}

// Load reads and validates a config file, filling in defaults.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if len(c.Sites) == 0 {
		return nil, errors.New("config has no sites")
	}
	seen := map[string]bool{}
	for i := range c.Sites {
		s := &c.Sites[i]
		if s.Name == "" || seen[s.Name] {
			return nil, fmt.Errorf("site %d: name empty or duplicate", i)
		}
		seen[s.Name] = true
		if u, err := url.Parse(s.URL); err != nil || u.Host == "" {
			return nil, fmt.Errorf("site %s: bad url %q", s.Name, s.URL)
		}
		if s.Interval == 0 {
			s.Interval = DefaultInterval
		}
		if s.Timeout == 0 {
			s.Timeout = DefaultTimeout
		}
		if s.Timeout >= s.Interval {
			return nil, fmt.Errorf("site %s: timeout must be shorter than interval", s.Name)
		}
	}
	return &c, nil
}
