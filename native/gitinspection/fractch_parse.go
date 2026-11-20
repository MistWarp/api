package gitinspection

import (
	"regexp"
	"strings"
)

const maxHiddenValueDepth = 64

type fractchStatement struct {
	Kind  string
	Key   string
	Name  string
	File  string
	Head  string
	At    string
	Body  string
	Norm  string
	lines []string
}

var (
	positionSuffix   = regexp.MustCompile(`\s+at\s+(-?[0-9.eE+-]+)\s*,\s*(-?[0-9.eE+-]+)\s*$`)
	definitionName   = regexp.MustCompile(`^def\s+(@?[^\s(]+)`)
	variableIDSuffix = regexp.MustCompile(`\s+id\s+("(?:[^"\\]|\\.)*")\s*;?\s*$`)
	firstWord        = regexp.MustCompile(`^[A-Za-z_]+`)
)

var topLevelKeywords = map[string]bool{
	"sprite": true, "stage": true, "var": true, "cloud": true, "use": true, "platform": true, "watch": true,
	"comment": true, "costume": true, "backdrop": true, "sound": true, "when": true, "def": true, "script": true,
}

func isFractchQuote(c byte) bool {
	return c == '"' || c == '`'
}

func isIdentifierByte(c byte) bool {
	return c == '_' || c == ')' || c == ']' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isCommentStart(src string, i int) bool {
	return strings.HasPrefix(src[i:], "//") || strings.HasPrefix(src[i:], "/*")
}

func skipFractchString(src string, i int) int {
	if strings.HasPrefix(src[i:], `"""`) {
		end := strings.Index(src[i+3:], `"""`)
		if end < 0 {
			return len(src)
		}
		return i + 3 + end + 3
	}
	quote := src[i]
	j := i + 1
	for j < len(src) && src[j] != quote && src[j] != '\n' {
		if src[j] == '\\' {
			j++
		}
		j++
	}
	if j < len(src) && src[j] == quote {
		j++
	}
	if j > len(src) {
		return len(src)
	}
	return j
}

func skipFractchComment(src string, i int) int {
	if strings.HasPrefix(src[i:], "//") {
		end := strings.IndexByte(src[i:], '\n')
		if end < 0 {
			return len(src)
		}
		return i + end
	}
	end := strings.Index(src[i+2:], "*/")
	if end < 0 {
		return len(src)
	}
	return i + 2 + end + 2
}

func skipSpace(src string, i int) int {
	for i < len(src) && (src[i] == ' ' || src[i] == '\t' || src[i] == '\r' || src[i] == '\n') {
		i++
	}
	return i
}

func startsTopLevelStatement(src string, i int) bool {
	word := firstWord.FindString(src[skipSpace(src, i):])
	return topLevelKeywords[word]
}

func readFractchStatement(src string, i int) int {
	braces, brackets := 0, 0
	for i < len(src) {
		c := src[i]
		if isFractchQuote(c) {
			i = skipFractchString(src, i)
			continue
		}
		if isCommentStart(src, i) {
			i = skipFractchComment(src, i)
			continue
		}
		i++
		switch c {
		case '{':
			braces++
		case '}':
			braces--
			if braces <= 0 {
				return i
			}
		case '(', '[':
			brackets++
		case ')', ']':
			if brackets > 0 {
				brackets--
			}
		case ';':
			if braces == 0 {
				return i
			}
		case '\n':
			if braces == 0 && brackets == 0 && startsTopLevelStatement(src, i) {
				return i
			}
		}
	}
	return i
}

func splitFractchStatements(src string) []string {
	statements := []string{}
	pending := []string{}
	flush := func() {
		if len(pending) > 0 {
			statements = append(statements, strings.Join(pending, "\n"))
			pending = pending[:0]
		}
	}
	i := 0
	for i < len(src) {
		next := skipSpace(src, i)
		if len(pending) > 0 && strings.Count(src[i:next], "\n") > 1 {
			flush()
		}
		i = next
		if i >= len(src) {
			break
		}
		if isCommentStart(src, i) {
			end := skipFractchComment(src, i)
			text := src[i:end]
			if !(strings.HasPrefix(text, "/**") && strings.Contains(text, "* target:")) {
				pending = append(pending, strings.TrimSpace(text))
			}
			i = end
			continue
		}
		end := readFractchStatement(src, i)
		text := strings.TrimSpace(src[i:end])
		if len(pending) > 0 {
			text = strings.Join(pending, "\n") + "\n" + text
			pending = pending[:0]
		}
		statements = append(statements, text)
		i = end
	}
	flush()
	return statements
}

func leadingFractchComments(text string) (string, string) {
	lines := strings.Split(text, "\n")
	inBlock := false
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if inBlock {
			if strings.Contains(trimmed, "*/") {
				inBlock = false
			}
			continue
		}
		if strings.HasPrefix(trimmed, "//") {
			continue
		}
		if strings.HasPrefix(trimmed, "/*") {
			inBlock = !strings.Contains(trimmed[2:], "*/")
			continue
		}
		return strings.Join(lines[:index], "\n"), strings.Join(lines[index:], "\n")
	}
	return text, ""
}

func scriptHeadEnd(text string) int {
	for i := 0; i < len(text); {
		c := text[i]
		if isFractchQuote(c) {
			i = skipFractchString(text, i)
			continue
		}
		if c == '{' {
			return i
		}
		i++
	}
	return len(text)
}

func fractchTokens(text string) []string {
	tokens := []string{}
	i := 0
	for i < len(text) {
		c := text[i]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == ';' {
			i++
			continue
		}
		start := i
		if isFractchQuote(c) {
			i = skipFractchString(text, i)
		} else {
			for i < len(text) && !strings.ContainsRune(" \t\r\n;", rune(text[i])) {
				if isFractchQuote(text[i]) {
					i = skipFractchString(text, i)
					continue
				}
				i++
			}
		}
		tokens = append(tokens, text[start:i])
	}
	return tokens
}

func unquoteFractch(token string) string {
	if len(token) >= 2 && isFractchQuote(token[0]) && token[len(token)-1] == token[0] {
		inner := token[1 : len(token)-1]
		if token[0] == '"' {
			inner = strings.ReplaceAll(inner, `\"`, `"`)
			inner = strings.ReplaceAll(inner, `\\`, `\`)
		}
		return inner
	}
	return token
}

var flagAttributes = map[string]bool{"hidden": true, "visible": true, "draggable": true, "current": true, "minimized": true, "continuous": true, "large": true, "slider": true}

func fractchAttributes(tokens []string) map[string]string {
	attributes := map[string]string{}
	for i := 0; i < len(tokens); i++ {
		word := tokens[i]
		if flagAttributes[word] {
			attributes[word] = "true"
			continue
		}
		if i+1 < len(tokens) {
			attributes[word] = tokens[i+1]
			i++
		}
	}
	return attributes
}

func maxParenDepth(src string) int {
	depth, deepest := 0, 0
	for i := 0; i < len(src); {
		c := src[i]
		if isFractchQuote(c) {
			i = skipFractchString(src, i)
			continue
		}
		if isCommentStart(src, i) {
			i = skipFractchComment(src, i)
			continue
		}
		if c == '(' {
			depth++
			if depth > deepest {
				deepest = depth
			}
		} else if c == ')' && depth > 0 {
			depth--
		}
		i++
	}
	return deepest
}

func stripHiddenValues(src string) string {
	if !strings.Contains(src, "??") || maxParenDepth(src) > maxHiddenValueDepth {
		return src
	}
	out, _, _ := stripHiddenFrom(src, 0, false)
	return out
}

func skipHiddenValue(src string, i int) int {
	depth := 0
	for i < len(src) {
		c := src[i]
		if isFractchQuote(c) {
			i = skipFractchString(src, i)
			continue
		}
		switch c {
		case '(', '[':
			depth++
		case ')', ']':
			if depth == 0 {
				return i
			}
			depth--
		case ',', ';', '{', '}', '\n':
			if depth == 0 {
				return i
			}
		}
		i++
	}
	return i
}

func stripHiddenFrom(src string, i int, inParen bool) (string, int, bool) {
	var builder strings.Builder
	hidden, comma := false, false
	for i < len(src) {
		c := src[i]
		if isFractchQuote(c) {
			end := skipFractchString(src, i)
			builder.WriteString(src[i:end])
			i = end
			continue
		}
		if isCommentStart(src, i) {
			end := skipFractchComment(src, i)
			builder.WriteString(src[i:end])
			i = end
			continue
		}
		if c == '(' {
			inner, next, stripped := stripHiddenFrom(src, i+1, true)
			before := byte(' ')
			if i > 0 {
				before = src[i-1]
			}
			after := nextNonSpace(src, next)
			standalone := !isIdentifierByte(before) && (after == 0 || strings.IndexByte(";,){]\n", after) >= 0)
			if stripped && standalone {
				builder.WriteString(inner)
			} else {
				builder.WriteString("(" + inner + ")")
			}
			i = next
			continue
		}
		if c == ')' && inParen {
			return builder.String(), i + 1, hidden && !comma
		}
		if c == ',' {
			comma = true
		}
		if strings.HasPrefix(src[i:], "??") {
			kept := strings.TrimRight(builder.String(), " \t")
			builder.Reset()
			builder.WriteString(kept)
			hidden = true
			i = skipHiddenValue(src, i+2)
			continue
		}
		builder.WriteByte(c)
		i++
	}
	return builder.String(), i, false
}

func nextNonSpace(src string, i int) byte {
	for i < len(src) {
		if src[i] != ' ' && src[i] != '\t' && src[i] != '\r' {
			return src[i]
		}
		i++
	}
	return 0
}

func camelIdentifiers(src string) string {
	var builder strings.Builder
	for i := 0; i < len(src); {
		c := src[i]
		if isFractchQuote(c) {
			end := skipFractchString(src, i)
			builder.WriteString(src[i:end])
			i = end
			continue
		}
		if isCommentStart(src, i) {
			end := skipFractchComment(src, i)
			builder.WriteString(src[i:end])
			i = end
			continue
		}
		if !(c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')) || (i > 0 && isIdentifierByte(src[i-1])) {
			builder.WriteByte(c)
			i++
			continue
		}
		end := i
		for end < len(src) && isIdentifierByte(src[end]) && src[end] != ')' && src[end] != ']' {
			end++
		}
		word := src[i:end]
		if c >= 'a' && c <= 'z' && strings.Contains(word, "_") {
			upper := false
			for index := 0; index < len(word); index++ {
				letter := word[index]
				if letter == '_' {
					upper = true
					continue
				}
				if upper && letter >= 'a' && letter <= 'z' {
					letter -= 'a' - 'A'
				}
				upper = false
				builder.WriteByte(letter)
			}
		} else {
			builder.WriteString(word)
		}
		i = end
	}
	return builder.String()
}

func parseFractchStatement(text, file string) fractchStatement {
	comments, code := leadingFractchComments(text)
	statement := fractchStatement{File: file, Body: text}
	if code == "" {
		statement.Kind = "comment"
		statement.Key = "note " + text
		statement.Name = firstLine(text)
		return statement
	}
	statement.Kind = firstWord.FindString(code)
	switch statement.Kind {
	case "when", "def", "script":
		headEnd := scriptHeadEnd(code)
		head := strings.TrimSpace(code[:headEnd])
		if match := positionSuffix.FindStringSubmatch(head); match != nil {
			statement.At = match[1] + "," + match[2]
			head = strings.TrimSpace(head[:len(head)-len(match[0])])
		}
		statement.Head = head
		statement.Key = head
		if match := definitionName.FindStringSubmatch(head); match != nil {
			statement.Key = "def " + match[1]
		}
		body := head + " " + strings.TrimSpace(code[headEnd:])
		if comments != "" {
			body = comments + "\n" + body
		}
		statement.Body = body
		statement.Norm = camelIdentifiers(stripHiddenValues(body))
		statement.Name = head
		statement.Kind = "script"
	case "var", "cloud":
		tokens := fractchTokens(code)
		if len(tokens) > 1 {
			statement.Name = unquoteFractch(tokens[1])
		}
		statement.Key = statement.Kind + " " + statement.Name
		if match := variableIDSuffix.FindStringSubmatch(code); match != nil {
			statement.Key = "id " + unquoteFractch(match[1])
		}
		statement.Body = code
	case "watch":
		tokens := fractchTokens(code)
		name := ""
		if len(tokens) > 2 && (tokens[1] == "var" || tokens[1] == "list") {
			name = tokens[1] + " " + unquoteFractch(tokens[2])
		} else if len(tokens) > 1 {
			name = tokens[1]
		}
		statement.Name = name
		statement.Key = "watch " + name
		if id, ok := fractchAttributes(tokens)["id"]; ok {
			statement.Key = "watch id " + unquoteFractch(id)
		}
		statement.Body = code
	case "sprite", "stage", "costume", "backdrop", "sound", "use", "platform", "comment":
		tokens := fractchTokens(code)
		if len(tokens) > 1 && isFractchQuote(tokens[1][0]) {
			statement.Name = unquoteFractch(tokens[1])
		}
		statement.Key = statement.Kind + " " + statement.Name
		switch statement.Kind {
		case "stage":
			statement.Key = "stage"
			statement.Name = "Stage"
		case "backdrop":
			statement.Key = "costume " + statement.Name
		case "comment":
			if statement.Name == "" {
				statement.Key = "comment " + code
			}
		}
		statement.Body = code
	default:
		statement.Kind = "other"
		statement.Key = code
		statement.Name = firstLine(code)
		statement.Body = code
	}
	return statement
}

func firstLine(text string) string {
	if index := strings.IndexByte(text, '\n'); index >= 0 {
		return text[:index]
	}
	return text
}

func parseFractchFile(src, file string) []fractchStatement {
	raw := splitFractchStatements(src)
	statements := make([]fractchStatement, 0, len(raw))
	for _, text := range raw {
		statements = append(statements, parseFractchStatement(text, file))
	}
	return statements
}
