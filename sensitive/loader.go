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

package sensitive

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
)

// wordsFile is the JSON schema for the production word list.
//
// File location (auto-discovered in order):
//  1. $OPENBOOK_SENSITIVE_WORDS env var (full path)
//  2. ./sensitive/words_zh.json
//  3. ./words_zh.json
//  4. /etc/openbook/sensitive_words.json
//
// On any failure, Check() still works (returns Blocked=false) — words.json
// is optional, the filter is a defense layer, not a hard requirement.
type wordsFile struct {
	Version    string              `json:"version"`
	Categories map[Category][]string `json:"categories"`
}

// LoadProductionWords loads the production word list from disk and registers
// it with the global checker. Safe to call multiple times — re-registration
// replaces the previous list.
//
// Returns nil on success or if the file is missing. Returns an error only
// if the file exists but is malformed (operator should fix it).
func LoadProductionWords() error {
	path := findWordsFile()
	if path == "" {
		log.Printf("[sensitive] no production word file found; running with empty list")
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		// File not readable — treat as empty rather than fatal.
		log.Printf("[sensitive] read %s failed: %v (continuing with empty list)", path, err)
		return nil
	}
	var wf wordsFile
	if err := json.Unmarshal(data, &wf); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	for cat, words := range wf.Categories {
		RegisterWords(cat, words)
	}
	log.Printf("[sensitive] loaded %d categories from %s (version %s)",
		len(wf.Categories), path, wf.Version)
	return nil
}

// findWordsFile locates the words.json file in priority order.
func findWordsFile() string {
	if p := os.Getenv("OPENBOOK_SENSITIVE_WORDS"); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	candidates := []string{
		"sensitive/words_zh.json",
		"words_zh.json",
		"/etc/openbook/sensitive_words.json",
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
}
