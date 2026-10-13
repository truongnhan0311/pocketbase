package extdb

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
)

// DatabaseConfig describes one external SQLite database file managed from the dashboard.
type DatabaseConfig struct {
	// Name is the unique identifier used in the API urls and the UI (e.g. "main", "ai").
	Name string `json:"name"`

	// Path is the SQLite database file path.
	// It may contain $VAR / ${VAR} env references and, when relative, is resolved against Config.BaseDir.
	Path string `json:"path"`

	// ReadOnly opens the database file in read-only mode (no edits or deletes at all).
	ReadOnly bool `json:"readonly"`

	// EditableTables lists the tables whose records can be updated ("*" for all tables).
	EditableTables []string `json:"editableTables"`

	// DeletableTables lists the tables whose records can be deleted ("*" for all tables).
	DeletableTables []string `json:"deletableTables"`
}

func (c DatabaseConfig) CanEdit(table string) bool {
	return !c.ReadOnly && (slices.Contains(c.EditableTables, "*") || slices.Contains(c.EditableTables, table))
}

func (c DatabaseConfig) CanDelete(table string) bool {
	return !c.ReadOnly && (slices.Contains(c.DeletableTables, "*") || slices.Contains(c.DeletableTables, table))
}

// HookConfig describes the main server endpoint notified after every record change.
type HookConfig struct {
	// URL receives a POST with the change payload. Empty disables the hook.
	URL string `json:"url"`

	// Secret signs the payload: X-ExtDB-Signature = hex(HMAC-SHA256(secret, timestamp + "." + body)).
	Secret string `json:"secret"`

	// Headers are extra request headers (e.g. an "api-key" required by the main server).
	Headers map[string]string `json:"headers"`

	// TimeoutSeconds is the hook request timeout (default 10).
	TimeoutSeconds int `json:"timeoutSeconds"`
}

// LogsConfig describes an external request logs table (in the familylink-api "system_logs" format)
// that is shown in the dashboard Logs page next to the PocketBase logs.
type LogsConfig struct {
	// Name is the unique identifier used in the API urls and the UI (e.g. "familylink").
	Name string `json:"name"`

	// Label is the name shown in the Logs page source switch (default Name).
	Label string `json:"label"`

	// Database is the name of the configured database holding the logs table.
	Database string `json:"database"`

	// Table is the logs table name (default "system_logs").
	Table string `json:"table"`
}

// Config defines the extdb plugin options.
type Config struct {
	// ConfigFile is an optional JSON file with the "databases" and "hook" settings.
	// Values loaded from the file are appended to/override the ones below.
	ConfigFile string `json:"-"`

	// BaseDir is the directory that relative database paths are resolved against
	// (it may contain env references; a relative BaseDir is resolved against the config file directory).
	// Defaults to the config file directory, or to the current working directory without a config file.
	// A non-empty value here takes precedence over the "baseDir" from the config file.
	BaseDir string `json:"baseDir"`

	Databases []DatabaseConfig `json:"databases"`
	Hook      HookConfig       `json:"hook"`
	Logs      []LogsConfig     `json:"logs"`
}

var nameRegex = regexp.MustCompile(`^[a-zA-Z0-9_\-]+$`)

func (c *Config) load() error {
	if c.ConfigFile != "" {
		raw, err := os.ReadFile(c.ConfigFile)
		if err != nil {
			return fmt.Errorf("extdb: failed to read config file: %w", err)
		}

		fileConfig := Config{}
		if err := json.Unmarshal(raw, &fileConfig); err != nil {
			return fmt.Errorf("extdb: invalid config file: %w", err)
		}

		c.Databases = append(c.Databases, fileConfig.Databases...)
		c.Logs = append(c.Logs, fileConfig.Logs...)
		if fileConfig.Hook.URL != "" {
			c.Hook = fileConfig.Hook
		}
		if c.BaseDir == "" {
			c.BaseDir = fileConfig.BaseDir
		}
	}

	configDir := "."
	if c.ConfigFile != "" {
		configDir = filepath.Dir(c.ConfigFile)
	}

	c.BaseDir = os.ExpandEnv(c.BaseDir)
	if c.BaseDir == "" {
		c.BaseDir = configDir
	} else if !filepath.IsAbs(c.BaseDir) {
		c.BaseDir = filepath.Join(configDir, c.BaseDir)
	}

	if c.Hook.TimeoutSeconds <= 0 {
		c.Hook.TimeoutSeconds = 10
	}

	seen := map[string]bool{}
	for i := range c.Databases {
		db := &c.Databases[i]
		if !nameRegex.MatchString(db.Name) {
			return fmt.Errorf("extdb: invalid database name %q (allowed a-z, 0-9, _ and -)", db.Name)
		}
		if seen[db.Name] {
			return fmt.Errorf("extdb: duplicated database name %q", db.Name)
		}
		db.Path = os.ExpandEnv(db.Path)
		if db.Path == "" {
			return errors.New("extdb: missing path for database " + db.Name)
		}
		if !filepath.IsAbs(db.Path) {
			db.Path = filepath.Join(c.BaseDir, db.Path)
		}
		if _, err := os.Stat(db.Path); err != nil {
			return fmt.Errorf("extdb: database %q file is not accessible: %w", db.Name, err)
		}
		seen[db.Name] = true
	}

	seenLogs := map[string]bool{}
	for i := range c.Logs {
		l := &c.Logs[i]
		if !nameRegex.MatchString(l.Name) {
			return fmt.Errorf("extdb: invalid logs name %q (allowed a-z, 0-9, _ and -)", l.Name)
		}
		if seenLogs[l.Name] {
			return fmt.Errorf("extdb: duplicated logs name %q", l.Name)
		}
		if !seen[l.Database] {
			return fmt.Errorf("extdb: logs %q references unknown database %q", l.Name, l.Database)
		}
		if l.Label == "" {
			l.Label = l.Name
		}
		if l.Table == "" {
			l.Table = "system_logs"
		}
		seenLogs[l.Name] = true
	}

	return nil
}
