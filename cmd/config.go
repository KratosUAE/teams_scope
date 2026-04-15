package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// Config is the merged view of the process environment plus an optional
// .env file in the current working directory. Fields use the same
// capitalisation as the PowerShell reference's .env keys so a single file
// can drive both runtimes.
type Config struct {
	TenantId     string
	ClientId     string
	ClientSecret string
	MongoUri     string
	ApiAddr      string
	ApiUrl       string
}

// Default values applied by loadConfig when neither .env nor the real
// environment provide a value.
const (
	defaultMongoUri = "mongodb://localhost:27017/teams_con"
	defaultApiAddr  = ":8080"
	defaultApiUrl   = "http://localhost:8080"
)

// loadConfig reads an optional .env file in CWD, then overlays real
// environment variables on top (real env wins), then applies defaults for
// the transport-level fields. No per-command validation is performed here
// — each subcommand checks only the fields it actually needs. This keeps
// `teams_con serve` runnable without Graph credentials.
func loadConfig() (*Config, error) {
	envFile, err := readDotEnv(".env")
	if err != nil {
		return nil, err
	}

	get := func(key string) string {
		if v, ok := os.LookupEnv(key); ok && v != "" {
			return v
		}
		return envFile[key]
	}

	cfg := &Config{
		TenantId:     get("TenantId"),
		ClientId:     get("ClientId"),
		ClientSecret: get("ClientSecret"),
		MongoUri:     get("MongoUri"),
		ApiAddr:      get("ApiAddr"),
		ApiUrl:       get("ApiUrl"),
	}

	if cfg.MongoUri == "" {
		cfg.MongoUri = defaultMongoUri
	}
	if cfg.ApiAddr == "" {
		cfg.ApiAddr = defaultApiAddr
	}
	if cfg.ApiUrl == "" {
		cfg.ApiUrl = defaultApiUrl
	}

	return cfg, nil
}

// readDotEnv parses a minimal KEY=VALUE file, matching the PowerShell
// reference's parser: blank lines and `#` comments are skipped, surrounding
// whitespace is trimmed, values are taken verbatim (no quote stripping).
// Returns an empty map (not an error) if the file does not exist.
func readDotEnv(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, fmt.Errorf("cmd: open %s: %w", path, err)
	}
	defer f.Close()

	out := make(map[string]string)
	sc := bufio.NewScanner(f)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			// Malformed lines are ignored rather than fatal — matches
			// permissive shell `source` semantics most users expect.
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		out[key] = val
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("cmd: read %s: %w", path, err)
	}
	return out, nil
}

// requireGraphCreds returns an error listing which Graph auth fields are
// missing. Used by `crawl` before it tries to mint a token.
func (c *Config) requireGraphCreds() error {
	var missing []string
	if c.TenantId == "" {
		missing = append(missing, "TenantId")
	}
	if c.ClientId == "" {
		missing = append(missing, "ClientId")
	}
	if c.ClientSecret == "" {
		missing = append(missing, "ClientSecret")
	}
	if len(missing) > 0 {
		return fmt.Errorf("cmd: missing required env vars: %s", strings.Join(missing, ", "))
	}
	return nil
}
