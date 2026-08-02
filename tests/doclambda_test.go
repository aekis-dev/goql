package tests

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/aekis-dev/goql"
)

// The docs are the contract, so every lambda they show must parse. This reads docs/*.md at
// test time rather than working from a copy, so an example edited in the documentation is
// checked on the next run instead of drifting away from a snapshot.
//
// Only the English pages are read: the translations carry the same code by policy (§22, with
// comments and user-facing strings translated), and TestDocs_TranslationsShareCode checks it.

var (
	goBlockRe = regexp.MustCompile("(?s)```go\n(.*?)```")
	callRe    = regexp.MustCompile(`goql\.(Select|Update|Delete|Insert|Exists)\s*(\[[^\]]*\])?\s*\(`)
	funcRe    = regexp.MustCompile(`func\s*\(`)
)

type docLambda struct {
	file       string
	line       int
	call       string
	src        string
	start, end int
	block      int
}

// matchBrace returns the index just past the '}' closing the one at i, skipping over string,
// rune and raw-string literals so a brace inside text does not desynchronise the count.
func matchBrace(s string, i int) int {
	depth := 0
	var inStr, inChar, inRaw bool
	for ; i < len(s); i++ {
		c := s[i]
		switch {
		case inRaw:
			if c == '`' {
				inRaw = false
			}
		case inStr:
			if c == '\\' {
				i++
			} else if c == '"' {
				inStr = false
			}
		case inChar:
			if c == '\\' {
				i++
			} else if c == '\'' {
				inChar = false
			}
		default:
			switch c {
			case '`':
				inRaw = true
			case '"':
				inStr = true
			case '\'':
				inChar = true
			case '{':
				depth++
			case '}':
				if depth--; depth == 0 {
					return i + 1
				}
			}
		}
	}
	return -1
}

func collectDocLambdas(t *testing.T) []docLambda {
	t.Helper()

	pages, err := filepath.Glob(filepath.Join("..", "docs", "*.md"))
	if err != nil {
		t.Fatal(err)
	}

	var found []docLambda
	for _, page := range pages {
		if strings.Contains(filepath.Base(page), ".es.") {
			continue
		}
		raw, err := os.ReadFile(page)
		if err != nil {
			t.Fatal(err)
		}
		text := string(raw)

		for _, blk := range goBlockRe.FindAllStringSubmatchIndex(text, -1) {
			block := text[blk[2]:blk[3]]
			line := strings.Count(text[:blk[0]], "\n") + 1

			for _, call := range callRe.FindAllStringSubmatchIndex(block, -1) {
				fn := funcRe.FindStringIndex(block[call[1]:])
				if fn == nil {
					continue
				}
				start := call[1] + fn[0]
				brace := strings.Index(block[call[1]+fn[1]:], "{")
				if brace < 0 {
					continue
				}
				end := matchBrace(block, call[1]+fn[1]+brace)
				if end < 0 {
					continue
				}

				src := block[start:end]
				// Elided fragments cannot parse, and ✗ marks a deliberately-invalid example.
				if strings.Contains(src, "…") || strings.Contains(src, "...") ||
					strings.Contains(src, "✗") {
					continue
				}
				found = append(found, docLambda{
					file: page, line: line, call: block[call[2]:call[3]],
					src: src, start: start, end: end, block: blk[0],
				})
			}
		}
	}

	// Keep only outermost lambdas. A nested one is parsed as part of its parent and cannot
	// stand alone — it may name a handle or params struct the enclosing lambda declares.
	var top []docLambda
	for _, a := range found {
		nested := false
		for _, b := range found {
			if b.block == a.block && b.start <= a.start && a.end <= b.end && b.start != a.start {
				nested = true
				break
			}
		}
		if !nested {
			top = append(top, a)
		}
	}
	return top
}

func TestDocs_EveryLambdaParses(t *testing.T) {
	lambdas := collectDocLambdas(t)
	if len(lambdas) < 30 {
		t.Fatalf("only %d lambdas found in the docs — the extractor is probably broken", len(lambdas))
	}

	for _, c := range lambdas {
		if _, err := (&goql.DebugExecutor{}).ParseQueryFromSource(c.src, c.call); err != nil {
			t.Errorf("%s:%d (%s) does not parse: %v\n%s",
				c.file, c.line, c.call, err, strings.TrimSpace(c.src))
		}
	}
	t.Logf("%d documented lambdas parsed", len(lambdas))
}

// stripCode removes comments and blanks out string contents, leaving the structure of the
// code: identifiers, calls, operators, field names. Comments and user-facing strings are
// translated on the Spanish pages; nothing else may be.
func stripCode(code string) string {
	var out strings.Builder
	for i := 0; i < len(code); {
		switch {
		case strings.HasPrefix(code[i:], "//"):
			j := strings.IndexByte(code[i:], '\n')
			if j < 0 {
				i = len(code)
			} else {
				i += j
			}
		case strings.HasPrefix(code[i:], "/*"):
			j := strings.Index(code[i:], "*/")
			if j < 0 {
				i = len(code)
			} else {
				i += j + 2
			}
		case code[i] == '"' || code[i] == '`':
			quote := code[i]
			out.WriteString(`"_"`)
			for i++; i < len(code) && code[i] != quote; i++ {
				if quote == '"' && code[i] == '\\' {
					i++
				}
			}
			i++
		default:
			out.WriteByte(code[i])
			i++
		}
	}

	var kept []string
	for _, line := range strings.Split(out.String(), "\n") {
		if line = strings.TrimRight(line, " \t"); strings.TrimSpace(line) != "" {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

// Code blocks teach the same code in both languages (design.md §22). Comments and
// user-facing string contents are translated; identifiers, calls and structure are not, so
// a reader comparing two pages sees the same query and the same generated SQL.
func TestDocs_TranslationsShareCode(t *testing.T) {
	pages, err := filepath.Glob(filepath.Join("..", "docs", "*.es.md"))
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) == 0 {
		t.Fatal("no Spanish pages found")
	}

	blocksOf := func(path string) []string {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, m := range goBlockRe.FindAllStringSubmatch(string(raw), -1) {
			out = append(out, m[1])
		}
		return out
	}

	for _, es := range pages {
		en := strings.Replace(es, ".es.md", ".md", 1)
		if _, err := os.Stat(en); err != nil {
			continue
		}
		esBlocks, enBlocks := blocksOf(es), blocksOf(en)
		if len(esBlocks) != len(enBlocks) {
			t.Errorf("%s has %d Go blocks, %s has %d",
				filepath.Base(es), len(esBlocks), filepath.Base(en), len(enBlocks))
			continue
		}
		for i := range esBlocks {
			if stripCode(esBlocks[i]) != stripCode(enBlocks[i]) {
				t.Errorf("%s: Go block %d differs from the English page", filepath.Base(es), i+1)
			}
		}
	}
}
