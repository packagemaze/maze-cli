package doctor

import "strings"

type yamlLine struct {
	indent int
	text   string
}

// parseYAML reads the subset of YAML that pnpm-workspace.yaml, .yarnrc.yml, and
// GitHub workflow files use: block mappings and sequences, plain and quoted
// scalars, block scalars (which it keeps as text), and flow collections of
// scalars. Unknown constructs are kept as text rather than failing.
func parseYAML(text string) any {
	lines := yamlLines(text)
	if len(lines) == 0 {
		return nil
	}
	value, _ := parseYAMLNode(lines, 0, lines[0].indent)
	return value
}

func yamlLines(text string) []yamlLine {
	var lines []yamlLine
	for _, raw := range strings.Split(text, "\n") {
		raw = strings.TrimRight(raw, "\r")
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || trimmed == "---" || trimmed == "..." {
			continue
		}
		indent := len(raw) - len(strings.TrimLeft(raw, " "))
		lines = append(lines, yamlLine{indent: indent, text: stripYAMLComment(trimmed)})
	}
	return lines
}

func stripYAMLComment(text string) string {
	index := indexOutsideQuotes(text, func(index int) bool {
		return text[index] == '#' && index > 0 && (text[index-1] == ' ' || text[index-1] == '\t')
	})
	if index >= 0 {
		return strings.TrimSpace(text[:index])
	}
	return text
}

func parseYAMLNode(lines []yamlLine, index int, indent int) (any, int) {
	if isYAMLSequenceItem(lines[index].text) {
		return parseYAMLSequence(lines, index, indent)
	}
	return parseYAMLMapping(lines, index, indent)
}

func isYAMLSequenceItem(text string) bool {
	return text == "-" || strings.HasPrefix(text, "- ")
}

func parseYAMLMapping(lines []yamlLine, index int, indent int) (map[string]any, int) {
	mapping := map[string]any{}
	for index < len(lines) {
		line := lines[index]
		if line.indent != indent || isYAMLSequenceItem(line.text) {
			break
		}
		key, rest, ok := splitYAMLEntry(line.text)
		if !ok {
			index++
			continue
		}
		index++
		switch {
		case rest == "" && index < len(lines) && lines[index].indent > indent:
			mapping[key], index = parseYAMLNode(lines, index, lines[index].indent)
		case rest == "" && index < len(lines) && lines[index].indent == indent && isYAMLSequenceItem(lines[index].text):
			mapping[key], index = parseYAMLSequence(lines, index, indent)
		case rest == "":
			mapping[key] = nil
		case strings.HasPrefix(rest, "|") || strings.HasPrefix(rest, ">"):
			mapping[key], index = parseYAMLBlockScalar(lines, index, indent)
		default:
			mapping[key] = parseYAMLScalar(rest, 0)
		}
	}
	return mapping, index
}

func parseYAMLBlockScalar(lines []yamlLine, index int, indent int) (string, int) {
	var parts []string
	for index < len(lines) && lines[index].indent > indent {
		parts = append(parts, lines[index].text)
		index++
	}
	return strings.Join(parts, "\n"), index
}

func parseYAMLSequence(lines []yamlLine, index int, indent int) ([]any, int) {
	items := []any{}
	for index < len(lines) {
		line := lines[index]
		if line.indent != indent || !isYAMLSequenceItem(line.text) {
			break
		}
		rest := strings.TrimSpace(strings.TrimPrefix(line.text, "-"))
		switch {
		case rest == "" && index+1 < len(lines) && lines[index+1].indent > indent:
			var item any
			item, index = parseYAMLNode(lines, index+1, lines[index+1].indent)
			items = append(items, item)
		case rest == "":
			items = append(items, nil)
			index++
		case yamlLooksLikeMapping(rest):
			column := indent + len(line.text) - len(rest)
			lines[index] = yamlLine{indent: column, text: rest}
			var item any
			item, index = parseYAMLMapping(lines, index, column)
			items = append(items, item)
		default:
			items = append(items, parseYAMLScalar(rest, 0))
			index++
		}
	}
	return items, index
}

func yamlLooksLikeMapping(text string) bool {
	_, _, ok := splitYAMLEntry(text)
	return ok && !strings.HasPrefix(text, "{") && !strings.HasPrefix(text, "[")
}

func splitYAMLEntry(text string) (string, string, bool) {
	index := indexOutsideQuotes(text, func(index int) bool {
		return text[index] == ':' && (index+1 == len(text) || text[index+1] == ' ')
	})
	if index < 0 {
		return "", "", false
	}
	key := unquoteYAML(strings.TrimSpace(text[:index]))
	if key == "" {
		return "", "", false
	}
	return key, strings.TrimSpace(text[index+1:]), true
}

func parseYAMLScalar(text string, depth int) any {
	switch {
	case depth >= maxFlowDepth && (strings.HasPrefix(text, "{") || strings.HasPrefix(text, "[")):
		return text
	case strings.HasPrefix(text, "{") && strings.HasSuffix(text, "}"):
		mapping := map[string]any{}
		for _, entry := range splitFlowItems(text[1 : len(text)-1]) {
			if key, rest, ok := splitYAMLEntry(entry); ok {
				mapping[key] = parseYAMLScalar(rest, depth+1)
			}
		}
		return mapping
	case strings.HasPrefix(text, "[") && strings.HasSuffix(text, "]"):
		items := []any{}
		for _, entry := range splitFlowItems(text[1 : len(text)-1]) {
			items = append(items, parseYAMLScalar(entry, depth+1))
		}
		return items
	case text == "true" || text == "True" || text == "TRUE":
		return true
	case text == "false" || text == "False" || text == "FALSE":
		return false
	case text == "~" || text == "null" || text == "Null":
		return nil
	default:
		return unquoteYAML(text)
	}
}

func unquoteYAML(text string) string {
	if len(text) >= 2 && text[0] == '"' && text[len(text)-1] == '"' {
		if parsed, ok := parseQuotedString(text); ok {
			return parsed
		}
		return text[1 : len(text)-1]
	}
	if len(text) >= 2 && text[0] == '\'' && text[len(text)-1] == '\'' {
		return strings.ReplaceAll(text[1:len(text)-1], "''", "'")
	}
	return text
}
