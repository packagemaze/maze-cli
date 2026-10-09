package doctor

import (
	"maps"
	"slices"
	"strings"
)

// The TOML and YAML readers both produce a tree of map[string]any, []any, and
// scalars; these accessors read either without caring which format wrote it.

func asMap(value any) map[string]any {
	mapping, _ := value.(map[string]any)
	return mapping
}

func asList(value any) []any {
	list, _ := value.([]any)
	return list
}

func asString(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case bool:
		if typed {
			return "true"
		}
		return "false"
	default:
		return ""
	}
}

func asBool(value any) bool {
	flag, _ := value.(bool)
	return flag
}

func asStrings(value any) []string {
	switch typed := value.(type) {
	case string:
		return []string{typed}
	case []any:
		values := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok {
				values = append(values, text)
			}
		}
		return values
	default:
		return nil
	}
}

// tables follows a key path and returns the array of tables there, or the one
// table written as [table] where a reader meant one entry.
func tables(root any, path ...string) []map[string]any {
	current := root
	for _, segment := range path {
		table := asMap(current)
		if table == nil {
			return nil
		}
		current = table[segment]
	}
	switch typed := current.(type) {
	case []any:
		result := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			if table := asMap(item); table != nil {
				result = append(result, table)
			}
		}
		return result
	case map[string]any:
		return []map[string]any{typed}
	default:
		return nil
	}
}

// sortedKeys gives map iteration a stable order so reports render the same way
// on every run.
func sortedKeys[V any](values map[string]V) []string {
	return slices.Sorted(maps.Keys(values))
}

// indexOutsideQuotes returns the first index accepted by match that is not
// inside a quoted string. Double quotes honor backslash escapes; single quotes
// are literal, as in both TOML and YAML.
func indexOutsideQuotes(text string, match func(index int) bool) int {
	var quote byte
	for index := 0; index < len(text); index++ {
		character := text[index]
		switch {
		case quote == '"' && character == '\\':
			index++
		case quote != 0 && character == quote:
			quote = 0
		case quote == 0 && (character == '"' || character == '\''):
			quote = character
		case quote == 0 && match(index):
			return index
		}
	}
	return -1
}
