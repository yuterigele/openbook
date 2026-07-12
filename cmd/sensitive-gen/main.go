// Build tool: 把 Sensitive-lexicon 项目转成 openbook 用的 words_zh.json
//
// 输入源（两种，按顺序合并 + 去重）：
//   1. TrChat 兼容 JSON（<lexicon-dir>/ThirdPartyCompatibleFormats/TrChat/SensitiveLexicon.json）
//   2. Vocabulary 目录下所有 .txt 文件（每行一个词）
//
// 用法：
//   go run ./cmd/sensitive-gen -in <lexicon-root> -out ./sensitive/words_zh.json
//
// 例如：
//   go run ./cmd/sensitive-gen \
//     -in C:\Users\Admin\Downloads\Sensitive-lexicon-main\Sensitive-lexicon-main \
//     -out ./sensitive/words_zh.json
//
// 分类策略：词库本身没有自动分类，所有词都进 "general" 一桶。
// 代码里仍然有 6 大类手工精选词（sensitive/sensitive.go 的 defaultWords），
// 用来兜底"即使 JSON 加载失败也至少能拦最高危的几个"。

package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type lexiconIn struct {
	LastUpdateDate string   `json:"lastUpdateDate"`
	Words          []string `json:"words"`
}

type wordsOut struct {
	Version    string              `json:"version"`
	Categories map[string][]string `json:"categories"`
	// SourceFiles records which files contributed, for traceability.
	SourceFiles []string `json:"source_files,omitempty"`
}

func main() {
	in := flag.String("in", "", "input lexicon root directory (contains Vocabulary/ and ThirdPartyCompatibleFormats/)")
	out := flag.String("out", "./sensitive/words_zh.json", "output words_zh.json path")
	flag.Parse()
	if *in == "" {
		log.Fatal("-in is required")
	}

	seen := map[string]struct{}{}
	var words []string
	var sourceFiles []string

	// Source 1: TrChat JSON
	trchatPath := filepath.Join(*in, "ThirdPartyCompatibleFormats", "TrChat", "SensitiveLexicon.json")
	if n, err := loadTrChat(trchatPath, seen, &words); err != nil {
		log.Printf("trchat load skipped: %v", err)
	} else {
		log.Printf("trchat: +%d words from %s", n, trchatPath)
		sourceFiles = append(sourceFiles, trchatPath)
	}

	// Source 2: Vocabulary/*.txt (one word per line)
	vocabDir := filepath.Join(*in, "Vocabulary")
	if n, err := loadVocabularyDir(vocabDir, seen, &words); err != nil {
		log.Printf("vocabulary load skipped: %v", err)
	} else {
		log.Printf("vocabulary: +%d words from %s", n, vocabDir)
		sourceFiles = append(sourceFiles, vocabDir+"/*")
	}

	// Sort for stable diffs.
	sort.Strings(words)

	doc := wordsOut{
		Version: fmt.Sprintf("v%s", time.Now().Format("2006.01.02")),
		Categories: map[string][]string{
			// One bucket holds everything; the code-defined defaultWords in
			// sensitive/sensitive.go still cover politics/porn/violence/ad/
			// abuse/illegal for high-confidence hits even when this JSON
			// is absent.
			"general": words,
		},
		SourceFiles: sourceFiles,
	}
	outData, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		log.Fatalf("marshal output: %v", err)
	}
	if err := os.WriteFile(*out, outData, 0644); err != nil {
		log.Fatalf("write output: %v", err)
	}
	log.Printf("wrote %d unique words to %s (version=%s)", len(words), *out, doc.Version)
}

// loadTrChat reads the TrChat JSON.
func loadTrChat(path string, seen map[string]struct{}, out *[]string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	var lex lexiconIn
	if err := json.Unmarshal(data, &lex); err != nil {
		return 0, fmt.Errorf("parse: %w", err)
	}
	added := 0
	for _, w := range lex.Words {
		w = trimSpace(w)
		if w == "" {
			continue
		}
		if _, ok := seen[w]; ok {
			continue
		}
		seen[w] = struct{}{}
		*out = append(*out, w)
		added++
	}
	return added, nil
}

// loadVocabularyDir reads every .txt file under `dir` (non-recursive) and
// adds one-word-per-line entries to the dedup map.
func loadVocabularyDir(dir string, seen map[string]struct{}, out *[]string) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	added := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !strings.HasSuffix(e.Name(), ".txt") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		f, err := os.Open(path)
		if err != nil {
			log.Printf("skip %s: %v", path, err)
			continue
		}
		scanner := bufio.NewScanner(f)
		// Allow long lines (Tencent 临时 list has some long compound words).
		buf := make([]byte, 0, 64*1024)
		scanner.Buffer(buf, 1024*1024)
		for scanner.Scan() {
			w := trimSpace(scanner.Text())
			if w == "" {
				continue
			}
			if _, ok := seen[w]; ok {
				continue
			}
			seen[w] = struct{}{}
			*out = append(*out, w)
			added++
		}
		f.Close()
		if err := scanner.Err(); err != nil {
			log.Printf("scan %s: %v", path, err)
		}
	}
	return added, nil
}

func trimSpace(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t' || s[0] == '\n' || s[0] == '\r' || s[0] == 0xEF && len(s) >= 3 && s[1] == 0xBB && s[2] == 0xBF) {
		// also strip UTF-8 BOM
		if s[0] == 0xEF && s[1] == 0xBB && s[2] == 0xBF {
			s = s[3:]
			continue
		}
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t' || s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}
