package knowledge

import (
	"errors"
	"strings"
	"unicode"
)

type TextChunkOptions struct {
	MaxRunes     int
	OverlapRunes int
}

func (o TextChunkOptions) validate() error {
	if o.MaxRunes < 128 || o.MaxRunes > 8000 {
		return errors.New("knowledge chunk max runes must be between 128 and 8000")
	}
	if o.OverlapRunes < 0 || o.OverlapRunes >= o.MaxRunes/2 {
		return errors.New("knowledge chunk overlap must be non-negative and less than half the chunk size")
	}
	return nil
}

// ChunkMarkdown creates a deterministic text/table baseline. Binary documents,
// OCR and VLM descriptions enter the same ChunkDraft contract in later slices.
func ChunkMarkdown(content string, options TextChunkOptions) ([]ChunkDraft, error) {
	if err := options.validate(); err != nil {
		return nil, err
	}
	content = strings.TrimSpace(strings.ReplaceAll(content, "\r\n", "\n"))
	if content == "" {
		return nil, errors.New("knowledge document content is required")
	}

	type block struct {
		sectionPath []string
		elementType ElementType
		text        string
	}
	var blocks []block
	var sectionPath []string
	var lines []string
	currentType := ElementText
	flush := func() {
		if len(lines) == 0 {
			return
		}
		separator := " "
		if currentType == ElementTable {
			separator = "\n"
		}
		text := strings.TrimSpace(strings.Join(lines, separator))
		if text != "" {
			blocks = append(blocks, block{
				sectionPath: append([]string(nil), sectionPath...),
				elementType: currentType,
				text:        text,
			})
		}
		lines = nil
	}

	for _, rawLine := range strings.Split(content, "\n") {
		line := strings.TrimSpace(rawLine)
		if level, title, ok := markdownHeading(line); ok {
			flush()
			if level > len(sectionPath)+1 {
				level = len(sectionPath) + 1
			}
			sectionPath = append(append([]string(nil), sectionPath[:level-1]...), title)
			continue
		}
		if line == "" {
			flush()
			continue
		}
		lineType := ElementText
		if isMarkdownTableLine(line) {
			lineType = ElementTable
		}
		if len(lines) > 0 && lineType != currentType {
			flush()
		}
		currentType = lineType
		lines = append(lines, line)
	}
	flush()

	chunks := make([]ChunkDraft, 0, len(blocks))
	for _, current := range blocks {
		for _, part := range splitRunes(current.text, options.MaxRunes, options.OverlapRunes) {
			searchSource := strings.TrimSpace(strings.Join(current.sectionPath, " ") + " " + part)
			searchText := NormalizeSearchText(searchSource)
			if searchText == "" {
				continue
			}
			chunks = append(chunks, ChunkDraft{
				ElementType:   current.elementType,
				SectionPath:   append([]string(nil), current.sectionPath...),
				ContentText:   part,
				SearchText:    searchText,
				ContentSHA256: SHA256Hex(part),
			})
		}
	}
	if len(chunks) == 0 {
		return nil, errors.New("knowledge document produced no searchable chunks")
	}
	return chunks, nil
}

func markdownHeading(line string) (int, string, bool) {
	level := 0
	for level < len(line) && level < 6 && line[level] == '#' {
		level++
	}
	if level == 0 || len(line) <= level || line[level] != ' ' {
		return 0, "", false
	}
	title := strings.TrimSpace(line[level+1:])
	return level, title, title != ""
}

func isMarkdownTableLine(line string) bool {
	return strings.Count(line, "|") >= 2
}

func splitRunes(value string, maxRunes, overlapRunes int) []string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) <= maxRunes {
		return []string{string(runes)}
	}
	result := make([]string, 0, (len(runes)+maxRunes-1)/maxRunes)
	for start := 0; start < len(runes); {
		end := start + maxRunes
		if end > len(runes) {
			end = len(runes)
		} else {
			minimum := start + maxRunes/2
			for candidate := end; candidate > minimum; candidate-- {
				if unicode.IsSpace(runes[candidate-1]) || strings.ContainsRune("。！？；.!?;\n", runes[candidate-1]) {
					end = candidate
					break
				}
			}
		}
		part := strings.TrimSpace(string(runes[start:end]))
		if part != "" {
			result = append(result, part)
		}
		if end == len(runes) {
			break
		}
		next := end - overlapRunes
		if next <= start {
			next = end
		}
		start = next
	}
	return result
}

// NormalizeSearchText emits lowercase word tokens and overlapping Han bigrams.
// The same normalization is used for stored chunks and parameterized tsquery input.
func NormalizeSearchText(value string) string {
	var tokens []string
	var word []rune
	var han []rune
	flushWord := func() {
		if len(word) > 0 {
			tokens = append(tokens, strings.ToLower(string(word)))
			word = nil
		}
	}
	flushHan := func() {
		if len(han) == 1 {
			tokens = append(tokens, string(han))
		}
		for i := 0; i+1 < len(han); i++ {
			tokens = append(tokens, string(han[i:i+2]))
		}
		han = nil
	}
	for _, current := range value {
		switch {
		case unicode.In(current, unicode.Han):
			flushWord()
			han = append(han, current)
		case unicode.IsLetter(current) || unicode.IsNumber(current):
			flushHan()
			word = append(word, current)
		default:
			flushWord()
			flushHan()
		}
	}
	flushWord()
	flushHan()
	return strings.Join(tokens, " ")
}

func BuildTSQuery(value string) (string, error) {
	normalized := NormalizeSearchText(value)
	if normalized == "" {
		return "", errors.New("knowledge search query is required")
	}
	seen := make(map[string]struct{})
	terms := make([]string, 0)
	for _, term := range strings.Fields(normalized) {
		if _, exists := seen[term]; exists {
			continue
		}
		seen[term] = struct{}{}
		terms = append(terms, term)
		if len(terms) > 64 {
			return "", errors.New("knowledge search query has too many terms")
		}
	}
	return strings.Join(terms, " | "), nil
}
