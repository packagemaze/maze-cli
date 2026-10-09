package doctor

import (
	"strconv"
	"strings"
)

// Flow collections deeper than this are kept as text. Nothing maze doctor reads
// nests more than two levels, and the cap keeps parsing linear in input size.
const maxFlowDepth = 8

// parseTOML reads the subset of TOML that package-client configuration uses:
// tables, arrays of tables, dotted and quoted keys, strings, booleans, numbers,
// inline tables, and arrays of scalars. Anything else is skipped line by line
// rather than failing, because a stranger's configuration file not matching a
// grammar is a coverage fact, not an error.
func parseTOML(text string) map[string]any {
	root := map[string]any{}
	current := root
	for _, rawLine := range strings.Split(text, "\n") {
		line := strings.TrimSpace(stripTOMLComment(rawLine))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[[") && strings.HasSuffix(line, "]]") {
			current = appendTOMLTable(root, tomlKeyPath(line[2:len(line)-2]))
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			current = tomlTable(root, tomlKeyPath(line[1:len(line)-1]))
			continue
		}
		key, value, ok := splitTOMLAssignment(line)
		if !ok {
			continue
		}
		assignTOML(current, key, value, 0)
	}
	return root
}

func assignTOML(table map[string]any, key string, rawValue string, depth int) {
	path := tomlKeyPath(key)
	if len(path) == 0 {
		return
	}
	if parsed, ok := parseTOMLValue(rawValue, depth); ok {
		tomlTable(table, path[:len(path)-1])[path[len(path)-1]] = parsed
	}
}

func stripTOMLComment(line string) string {
	if index := indexOutsideQuotes(line, func(index int) bool { return line[index] == '#' }); index >= 0 {
		return line[:index]
	}
	return line
}

func splitTOMLAssignment(line string) (string, string, bool) {
	index := indexOutsideQuotes(line, func(index int) bool { return line[index] == '=' })
	if index < 0 {
		return "", "", false
	}
	return strings.TrimSpace(line[:index]), strings.TrimSpace(line[index+1:]), true
}

func tomlKeyPath(key string) []string {
	var path []string
	var current strings.Builder
	var quote byte
	flush := func() {
		if segment := strings.TrimSpace(current.String()); segment != "" {
			path = append(path, segment)
		}
		current.Reset()
	}
	for index := 0; index < len(key); index++ {
		character := key[index]
		switch {
		case quote != 0 && character == quote:
			quote = 0
		case quote == 0 && (character == '"' || character == '\''):
			quote = character
		case quote == 0 && character == '.':
			flush()
		default:
			current.WriteByte(character)
		}
	}
	flush()
	return path
}

func tomlTable(root map[string]any, path []string) map[string]any {
	current := root
	for _, segment := range path {
		switch existing := current[segment].(type) {
		case map[string]any:
			current = existing
		case []any:
			if len(existing) == 0 {
				table := map[string]any{}
				current[segment] = []any{table}
				current = table
				continue
			}
			table, ok := existing[len(existing)-1].(map[string]any)
			if !ok {
				table = map[string]any{}
				existing[len(existing)-1] = table
			}
			current = table
		default:
			table := map[string]any{}
			current[segment] = table
			current = table
		}
	}
	return current
}

func appendTOMLTable(root map[string]any, path []string) map[string]any {
	if len(path) == 0 {
		return root
	}
	parent := tomlTable(root, path[:len(path)-1])
	table := map[string]any{}
	name := path[len(path)-1]
	existing, _ := parent[name].([]any)
	parent[name] = append(existing, table)
	return table
}

func parseTOMLValue(raw string, depth int) (any, bool) {
	value := strings.TrimSpace(raw)
	switch {
	case value == "":
		return nil, false
	case strings.HasPrefix(value, `"""`) || strings.HasPrefix(value, `'''`):
		return nil, false
	case strings.HasPrefix(value, `"`):
		return parseQuotedString(value)
	case strings.HasPrefix(value, `'`):
		end := strings.IndexByte(value[1:], '\'')
		if end < 0 {
			return nil, false
		}
		return value[1 : end+1], true
	case value == "true":
		return true, true
	case value == "false":
		return false, true
	case depth >= maxFlowDepth && (strings.HasPrefix(value, "{") || strings.HasPrefix(value, "[")):
		return value, true
	case strings.HasPrefix(value, "{"):
		return parseTOMLInlineTable(value, depth+1)
	case strings.HasPrefix(value, "["):
		return parseTOMLArray(value, depth+1)
	}
	if number, err := strconv.ParseFloat(strings.ReplaceAll(value, "_", ""), 64); err == nil {
		return number, true
	}
	return value, true
}

// parseQuotedString reads a double-quoted string with backslash escapes, the
// form TOML calls a basic string and YAML a double-quoted scalar.
func parseQuotedString(value string) (string, bool) {
	var result strings.Builder
	for index := 1; index < len(value); index++ {
		character := value[index]
		switch character {
		case '"':
			return result.String(), true
		case '\\':
			if index+1 >= len(value) {
				return "", false
			}
			index++
			switch value[index] {
			case 'n':
				result.WriteByte('\n')
			case 't':
				result.WriteByte('\t')
			case 'r':
				result.WriteByte('\r')
			case 'u', 'U':
				length := 4
				if value[index] == 'U' {
					length = 8
				}
				if index+length >= len(value) {
					return "", false
				}
				code, err := strconv.ParseUint(value[index+1:index+1+length], 16, 32)
				if err != nil {
					return "", false
				}
				result.WriteRune(rune(code))
				index += length
			default:
				result.WriteByte(value[index])
			}
		default:
			result.WriteByte(character)
		}
	}
	return "", false
}

func parseTOMLInlineTable(value string, depth int) (map[string]any, bool) {
	if !strings.HasSuffix(value, "}") {
		return nil, false
	}
	table := map[string]any{}
	for _, entry := range splitFlowItems(value[1 : len(value)-1]) {
		if key, raw, ok := splitTOMLAssignment(entry); ok {
			assignTOML(table, key, raw, depth)
		}
	}
	return table, true
}

func parseTOMLArray(value string, depth int) ([]any, bool) {
	if !strings.HasSuffix(value, "]") {
		return nil, false
	}
	items := []any{}
	for _, entry := range splitFlowItems(value[1 : len(value)-1]) {
		if parsed, ok := parseTOMLValue(entry, depth); ok {
			items = append(items, parsed)
		}
	}
	return items, true
}

// splitFlowItems splits the inside of a flow collection on top-level commas,
// returning substrings of the input so nesting costs no copies.
func splitFlowItems(value string) []string {
	var parts []string
	start := 0
	depth := 0
	var quote byte
	for index := 0; index < len(value); index++ {
		character := value[index]
		switch {
		case quote == '"' && character == '\\':
			index++
		case quote != 0 && character == quote:
			quote = 0
		case quote == 0 && (character == '"' || character == '\''):
			quote = character
		case quote == 0 && (character == '{' || character == '['):
			depth++
		case quote == 0 && (character == '}' || character == ']'):
			depth--
		case quote == 0 && depth == 0 && character == ',':
			parts = append(parts, strings.TrimSpace(value[start:index]))
			start = index + 1
		}
	}
	if last := strings.TrimSpace(value[start:]); last != "" {
		parts = append(parts, last)
	}
	return parts
}
