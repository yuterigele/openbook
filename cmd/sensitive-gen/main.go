// Build tool: 把第三方词库（TrChat 格式）转成 openbook 用的 words_zh.json
//
// 一次性脚本：跑完生成 JSON 后可删。
// 用法（在 D:\golang\openbook 目录）：
//   go run ./cmd/sensitive-gen -in <path-to-SensitiveLexicon.json> -out ./sensitive/words_zh.json
//
// 默认分类策略：词库没有自带分类，所有词都进 "general" 一桶。
// 代码里仍然有 6 大类手工精选词（sensitive/sensitive.go 的 defaultWords），
// 用来兜底"即使 JSON 加载失败也至少能拦最高危的几个"。

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"time"
)

type lexiconIn struct {
	LastUpdateDate string   `json:"lastUpdateDate"`
	Words          []string `json:"words"`
}

type wordsOut struct {
	Version    string              `json:"version"`
	Categories map[string][]string `json:"categories"`
}

func main() {
	in := flag.String("in", "", "input SensitiveLexicon.json path")
	out := flag.String("out", "./sensitive/words_zh.json", "output words_zh.json path")
	flag.Parse()
	if *in == "" {
		log.Fatal("-in is required")
	}
	data, err := os.ReadFile(*in)
	if err != nil {
		log.Fatalf("read input: %v", err)
	}
	var lex lexiconIn
	if err := json.Unmarshal(data, &lex); err != nil {
		log.Fatalf("parse input: %v", err)
	}
	// Filter empties + dedupe.
	seen := map[string]struct{}{}
	words := make([]string, 0, len(lex.Words))
	for _, w := range lex.Words {
		w = trimSpace(w)
		if w == "" {
			continue
		}
		if _, ok := seen[w]; ok {
			continue
		}
		seen[w] = struct{}{}
		words = append(words, w)
	}
	doc := wordsOut{
		Version: fmt.Sprintf("v%s from %s", time.Now().Format("2006.01.02"), lex.LastUpdateDate),
		Categories: map[string][]string{
			// One bucket holds everything; the code-defined defaultWords in
			// sensitive/sensitive.go still cover politics/porn/violence/ad/
			// abuse/illegal for high-confidence hits even when this JSON
			// is absent.
			"general": words,
		},
	}
	outData, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		log.Fatalf("marshal output: %v", err)
	}
	if err := os.WriteFile(*out, outData, 0644); err != nil {
		log.Fatalf("write output: %v", err)
	}
	log.Printf("wrote %d words to %s (version=%s)", len(words), *out, doc.Version)
}

func trimSpace(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t' || s[0] == '\n' || s[0] == '\r') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t' || s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}
