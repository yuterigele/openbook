/*
 * Copyright 2026 CloudWeGo Authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package chatmodel

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// envFileName is the config file name searched for. It can be a relative path
// (resolved against the process working directory) or absolute.
const envFileName = ".env"

var (
	envLoadOnce sync.Once
	envLoadErr  error
)

// LoadEnv parses a .env file (if present) and applies its key=value pairs to
// the environment. Existing environment variables always take precedence over
// file values, so a .env file never overrides what was explicitly set in the
// shell. It runs at most once per process via sync.Once and is safe to call
// from multiple entry points (main.go, cmd/chXX).
//
// Call it as early as possible in main, BEFORE any code reads environment
// variables (e.g. before msgops.KindFromEnv), so that file-provided values are
// visible everywhere.
func LoadEnv() {
	envLoadOnce.Do(func() {
		path := strings.TrimSpace(os.Getenv("ENV_FILE"))
		if path == "" {
			path = envFileName
		}
		if abs, err := filepath.Abs(path); err == nil {
			path = abs
		}

		f, err := os.Open(path)
		if err != nil {
			// Missing file is fine: fall back to plain environment variables.
			return
		}
		defer f.Close()

		m, perr := parseDotEnv(f)
		if perr != nil {
			envLoadErr = perr
			return
		}
		for k, v := range m {
			if _, ok := os.LookupEnv(k); ok {
				// Don't clobber values already set in the real environment.
				continue
			}
			_ = os.Setenv(k, v)
		}
	})
}

// loadDotEnv is kept as an internal alias for backward compatibility within
// this package (e.g. NewModel still calls it defensively).
func loadDotEnv() {
	LoadEnv()
}

// parseDotEnv reads a .env file and returns its key/value pairs. Supports:
//   - comments starting with '#'
//   - optional surrounding whitespace
//   - quoted values ("..." or '..."), with the matching quote stripped
//
// It intentionally does NOT interpolate variables or expand escape sequences,
// keeping behavior predictable for secrets like API keys.
func parseDotEnv(f *os.File) (map[string]string, error) {
	m := make(map[string]string)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Strip an inline "export " prefix, common in shell-style .env files.
		line = strings.TrimPrefix(line, "export ")

		idx := strings.IndexByte(line, '=')
		if idx <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])

		if n := len(val); n >= 2 {
			first, last := val[0], val[n-1]
			if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
				val = val[1 : n-1]
			}
		}
		m[key] = val
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return m, nil
}
