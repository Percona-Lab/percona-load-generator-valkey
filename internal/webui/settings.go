package webui

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/config"
)

var settingNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 _.-]{0,63}$`)

// SettingsStore keeps named configurations as YAML files.
type SettingsStore struct{ dir string }

// NewSettingsStore opens (creating if needed) the directory.
func NewSettingsStore(dir string) (*SettingsStore, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &SettingsStore{dir: dir}, nil
}

func (s *SettingsStore) path(name string) (string, error) {
	if !settingNameRe.MatchString(name) {
		return "", errors.New("name must be 1-64 characters: letters, digits, space, dot, dash or underscore, starting with a letter or digit")
	}
	return filepath.Join(s.dir, name+".yaml"), nil
}

// List returns the saved names, sorted.
func (s *SettingsStore) List() ([]string, error) {
	files, err := filepath.Glob(filepath.Join(s.dir, "*.yaml"))
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, f := range files {
		n := strings.TrimSuffix(filepath.Base(f), ".yaml")
		if settingNameRe.MatchString(n) {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out, nil
}

// Get loads a saved configuration.
func (s *SettingsStore) Get(name string) (config.Config, error) {
	p, err := s.path(name)
	if err != nil {
		return config.Config{}, ErrNotFound
	}
	c, err := config.Load(p)
	if errors.Is(err, os.ErrNotExist) {
		return config.Config{}, ErrNotFound
	}
	return c, err
}

// Put validates and saves a configuration. Credentials are never saved.
func (s *SettingsStore) Put(name string, c config.Config) error {
	p, err := s.path(name)
	if err != nil {
		return err
	}
	c.Normalize()
	if err := c.Validate(); err != nil {
		return err
	}
	return c.Save(p)
}

// Delete removes a saved configuration.
func (s *SettingsStore) Delete(name string) error {
	p, err := s.path(name)
	if err != nil {
		return ErrNotFound
	}
	if err := os.Remove(p); errors.Is(err, os.ErrNotExist) {
		return ErrNotFound
	} else {
		return err
	}
}
