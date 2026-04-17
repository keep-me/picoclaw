package tools

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/pmezard/go-difflib/difflib"
)

const (
	DiffFormatUnified = "unified"
	DiffFormatSideBySide = "side_by_side"
)

type DiffTool struct {
	fs              fileSystem
	maxReadFileSize int64
}

func NewDiffTool(
	workspace string,
	restrict bool,
	maxReadFileSize int,
	allowPaths ...[]*regexp.Regexp,
) *DiffTool {
	var patterns []*regexp.Regexp
	if len(allowPaths) > 0 {
		patterns = allowPaths[0]
	}

	maxSize := int64(maxReadFileSize)
	if maxSize <= 0 {
		maxSize = MaxReadFileSize
	}

	return &DiffTool{
		fs:              buildFs(workspace, restrict, patterns),
		maxReadFileSize: maxSize,
	}
}

func (t *DiffTool) Name() string {
	return "diff_files"
}

func (t *DiffTool) Description() string {
	return "Compare two text files and show their differences. Supports unified diff format and side-by-side comparison. Can ignore whitespace differences."
}

func (t *DiffTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"file_a": map[string]any{
				"type":        "string",
				"description": "Path to the first file (base/original file)",
			},
			"file_b": map[string]any{
				"type":        "string",
				"description": "Path to the second file (modified file)",
			},
			"format": map[string]any{
				"type":        "string",
				"description": "Output format: 'unified' for unified diff format (like git diff), or 'side_by_side' for side-by-side comparison. Default: 'unified'",
				"enum":        []string{"unified", "side_by_side"},
				"default":     "unified",
			},
			"ignore_whitespace": map[string]any{
				"type":        "boolean",
				"description": "If true, ignore whitespace differences (spaces, tabs, newlines). Default: false",
				"default":     false,
			},
			"context_lines": map[string]any{
				"type":        "integer",
				"description": "Number of context lines to show around each change. Only applicable for unified format. Default: 3",
				"default":     3,
			},
		},
		"required": []string{"file_a", "file_b"},
	}
}

func (t *DiffTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	fileA, ok := args["file_a"].(string)
	if !ok {
		return ErrorResult("file_a is required")
	}

	fileB, ok := args["file_b"].(string)
	if !ok {
		return ErrorResult("file_b is required")
	}

	format := DiffFormatUnified
	if rawFormat, exists := args["format"]; exists && rawFormat != nil {
		if f, ok := rawFormat.(string); ok {
			switch strings.ToLower(f) {
			case DiffFormatSideBySide:
				format = DiffFormatSideBySide
			case DiffFormatUnified:
				format = DiffFormatUnified
			}
		}
	}

	ignoreWhitespace := false
	if rawIgnore, exists := args["ignore_whitespace"]; exists && rawIgnore != nil {
		if ig, ok := rawIgnore.(bool); ok {
			ignoreWhitespace = ig
		}
	}

	contextLines := int64(3)
	if rawContext, exists := args["context_lines"]; exists && rawContext != nil {
		var err error
		contextLines, err = getInt64Arg(args, "context_lines", 3)
		if err != nil {
			return ErrorResult(err.Error())
		}
		if contextLines < 0 {
			contextLines = 0
		}
	}

	contentA, err := t.readFile(fileA)
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to read file_a: %v", err))
	}

	contentB, err := t.readFile(fileB)
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to read file_b: %v", err))
	}

	linesA := strings.SplitAfter(contentA, "\n")
	linesB := strings.SplitAfter(contentB, "\n")

	if len(linesA) > 0 && !strings.HasSuffix(linesA[len(linesA)-1], "\n") {
		linesA[len(linesA)-1] += "\n"
	}
	if len(linesB) > 0 && !strings.HasSuffix(linesB[len(linesB)-1], "\n") {
		linesB[len(linesB)-1] += "\n"
	}

	if ignoreWhitespace {
		linesA = normalizeLinesWhitespace(linesA)
		linesB = normalizeLinesWhitespace(linesB)
	}

	var result string
	switch format {
	case DiffFormatSideBySide:
		result = t.formatSideBySide(linesA, linesB, fileA, fileB, ignoreWhitespace)
	default:
		result, err = t.formatUnifiedDiff(linesA, linesB, fileA, fileB, int(contextLines))
		if err != nil {
			return ErrorResult(fmt.Sprintf("failed to generate diff: %v", err))
		}
	}

	return NewToolResult(result)
}

func (t *DiffTool) readFile(path string) (string, error) {
	file, err := t.fs.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	if info, statErr := file.Stat(); statErr == nil {
		if info.Size() > t.maxReadFileSize {
			return "", fmt.Errorf("file too large (max %d bytes)", t.maxReadFileSize)
		}
	}

	var buf bytes.Buffer
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		buf.WriteString(scanner.Text())
		buf.WriteByte('\n')
	}

	if err := scanner.Err(); err != nil {
		return "", err
	}

	return buf.String(), nil
}

func (t *DiffTool) formatUnifiedDiff(linesA, linesB []string, fileA, fileB string, contextLines int) (string, error) {
	cleanLinesA := make([]string, len(linesA))
	cleanLinesB := make([]string, len(linesB))

	for i, line := range linesA {
		cleanLinesA[i] = strings.TrimRight(line, "\r\n")
	}
	for i, line := range linesB {
		cleanLinesB[i] = strings.TrimRight(line, "\r\n")
	}

	unifiedDiff := difflib.UnifiedDiff{
		A:        cleanLinesA,
		B:        cleanLinesB,
		FromFile: filepath.Base(fileA),
		ToFile:   filepath.Base(fileB),
		Context:  contextLines,
	}

	var buf bytes.Buffer
	err := difflib.WriteUnifiedDiff(&buf, unifiedDiff)
	if err != nil {
		return "", err
	}

	result := buf.String()
	if result == "" {
		return fmt.Sprintf("No differences found between %s and %s", filepath.Base(fileA), filepath.Base(fileB)), nil
	}

	return result, nil
}

func (t *DiffTool) formatSideBySide(linesA, linesB []string, fileA, fileB string, ignoreWhitespace bool) string {
	const maxWidth = 80
	const columnWidth = (maxWidth - 3) / 2

	cleanLinesA := make([]string, len(linesA))
	cleanLinesB := make([]string, len(linesB))

	for i, line := range linesA {
		cleanLinesA[i] = strings.TrimRight(line, "\r\n")
	}
	for i, line := range linesB {
		cleanLinesB[i] = strings.TrimRight(line, "\r\n")
	}

	var result strings.Builder

	headerA := truncateString(filepath.Base(fileA), columnWidth)
	headerB := truncateString(filepath.Base(fileB), columnWidth)
	result.WriteString(fmt.Sprintf("%-*s | %s\n", columnWidth, headerA, headerB))
	result.WriteString(strings.Repeat("-", columnWidth) + "|" + strings.Repeat("-", columnWidth) + "\n")

	matcher := difflib.NewMatcher(cleanLinesA, cleanLinesB)
	blocks := matcher.GetMatchingBlocks()

	lineA := 0
	lineB := 0

	for _, block := range blocks {
		for lineA < block.A || lineB < block.B {
			var lineContentA, lineContentB string
			var statusA, statusB string

			if lineA < block.A && lineB < block.B {
				lineContentA = cleanLinesA[lineA]
				lineContentB = cleanLinesB[lineB]

				normA := normalizeLineWhitespace(lineContentA)
				normB := normalizeLineWhitespace(lineContentB)

				if normA == normB {
					statusA = "~"
					statusB = "~"
				} else {
					statusA = "?"
					statusB = "?"
				}

				lineA++
				lineB++
			} else if lineA < block.A {
				lineContentA = cleanLinesA[lineA]
				lineContentB = ""
				statusA = "-"
				statusB = " "
				lineA++
			} else {
				lineContentA = ""
				lineContentB = cleanLinesB[lineB]
				statusA = " "
				statusB = "+"
				lineB++
			}

			displayA := truncateString(lineContentA, columnWidth-2)
			displayB := truncateString(lineContentB, columnWidth-2)

			result.WriteString(fmt.Sprintf("%s %-*s | %s %s\n",
				statusA, columnWidth-2, displayA, statusB, displayB))
		}

		for i := 0; i < block.Size; i++ {
			lineContentA := cleanLinesA[lineA]
			lineContentB := cleanLinesB[lineB]

			displayA := truncateString(lineContentA, columnWidth-2)
			displayB := truncateString(lineContentB, columnWidth-2)

			result.WriteString(fmt.Sprintf("  %-*s |   %s\n",
				columnWidth-2, displayA, displayB))

			lineA++
			lineB++
		}
	}

	return result.String()
}

func normalizeLineWhitespace(line string) string {
	line = strings.ReplaceAll(line, "\t", " ")
	line = strings.ReplaceAll(line, "\r", "")
	line = strings.TrimSpace(line)

	for strings.Contains(line, "  ") {
		line = strings.ReplaceAll(line, "  ", " ")
	}

	return line
}

func normalizeLinesWhitespace(lines []string) []string {
	normalized := make([]string, len(lines))
	for i, line := range lines {
		normalized[i] = normalizeLineWhitespace(line)
	}
	return normalized
}

func truncateString(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen-3]) + "..."
}
