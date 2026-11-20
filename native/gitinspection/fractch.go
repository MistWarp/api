package gitinspection

import (
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	maxStateValueChars   = 160
	maxCrossPairProducts = 40_000
	crossPairSimilarity  = 0.6
)

type fractchStatement struct {
	Kind string
	Key  string
	Name string
	Text string
	Head string
	At   string
	Body string
	Norm string
}

type fractchScriptChange struct {
	Change   string `json:"change"`
	Kind     string `json:"kind"`
	Head     string `json:"head"`
	Before   string `json:"before,omitempty"`
	After    string `json:"after,omitempty"`
	BeforeAt string `json:"beforeAt,omitempty"`
	AfterAt  string `json:"afterAt,omitempty"`
}

type fractchItemChange struct {
	Change string `json:"change"`
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
}

type fractchStateChange struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Field     string `json:"field"`
	Before    string `json:"before"`
	After     string `json:"after"`
	Truncated bool   `json:"truncated,omitempty"`
}

type fractchFileChanges struct {
	Scripts []fractchScriptChange `json:"scripts"`
	Items   []fractchItemChange   `json:"items"`
	State   []fractchStateChange  `json:"state"`
	assets  assetLookup
}

type assetLookup struct {
	before func(string) string
	after  func(string) string
}

func noAssets(string) string {
	return ""
}

func isIdentifierByte(c byte) bool {
	return c == '_' || c == ')' || c == ']' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func stripHiddenValues(src string) string {
	out, _, _ := stripHiddenFrom(src, 0, false)
	return out
}

func stripHiddenFrom(src string, i int, inParen bool) (string, int, bool) {
	var builder strings.Builder
	hidden := -1
	commaAfterHidden := false
	for i < len(src) {
		c := src[i]
		if isFractchQuote(c) {
			end := skipFractchString(src, i)
			builder.WriteString(src[i:end])
			i = end
			continue
		}
		if strings.HasPrefix(src[i:], "//") || strings.HasPrefix(src[i:], "/*") {
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
			content := builder.String()
			if hidden >= 0 && !commaAfterHidden {
				return strings.TrimSpace(content[:hidden]), i + 1, true
			}
			return content, i + 1, false
		}
		if c == ',' && hidden >= 0 {
			commaAfterHidden = true
		}
		if inParen && hidden < 0 && strings.HasPrefix(src[i:], "??") {
			hidden = builder.Len()
		}
		builder.WriteByte(c)
		i++
	}
	return builder.String(), i, false
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
		if strings.HasPrefix(src[i:], "//") || strings.HasPrefix(src[i:], "/*") {
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
		for end < len(src) && (isIdentifierByte(src[end]) && src[end] != ')' && src[end] != ']') {
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

func nextNonSpace(src string, i int) byte {
	for i < len(src) {
		if src[i] != ' ' && src[i] != '\t' && src[i] != '\r' {
			return src[i]
		}
		i++
	}
	return 0
}

var (
	positionSuffix   = regexp.MustCompile(`\s+at\s+(-?[0-9.eE+-]+)\s*,\s*(-?[0-9.eE+-]+)\s*$`)
	definitionName   = regexp.MustCompile(`^def\s+(@?[^\s(]+)`)
	variableIDSuffix = regexp.MustCompile(`\s+id\s+("(?:[^"\\]|\\.)*")\s*;?\s*$`)
	firstWord        = regexp.MustCompile(`^[A-Za-z_]+`)
)

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

func isFractchQuote(c byte) bool {
	return c == '"' || c == '`'
}

func splitFractchStatements(src string) []string {
	statements := []string{}
	pending := []string{}
	i := 0
	for i < len(src) {
		c := src[i]
		if c == ' ' || c == '\t' || c == '\r' || c == '\n' {
			i++
			continue
		}
		if strings.HasPrefix(src[i:], "//") || strings.HasPrefix(src[i:], "/*") {
			end := skipFractchComment(src, i)
			text := src[i:end]
			if !(strings.HasPrefix(text, "/**") && strings.Contains(text, "* target:")) {
				pending = append(pending, strings.TrimSpace(text))
			}
			i = end
			continue
		}
		start := i
		depth := 0
		for i < len(src) {
			c = src[i]
			if isFractchQuote(c) {
				i = skipFractchString(src, i)
				continue
			}
			if strings.HasPrefix(src[i:], "//") || strings.HasPrefix(src[i:], "/*") {
				i = skipFractchComment(src, i)
				continue
			}
			i++
			if c == '{' {
				depth++
			} else if c == '}' {
				depth--
				if depth <= 0 {
					break
				}
			} else if c == ';' && depth == 0 {
				break
			}
		}
		text := strings.TrimSpace(src[start:i])
		if len(pending) > 0 {
			text = strings.Join(pending, "\n") + "\n" + text
			pending = pending[:0]
		}
		statements = append(statements, text)
	}
	if len(pending) > 0 {
		statements = append(statements, strings.Join(pending, "\n"))
	}
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

var flagAttributes = map[string]bool{"hidden": true, "draggable": true, "current": true, "minimized": true, "continuous": true, "large": true, "slider": true}

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

func parseFractchStatement(text string) fractchStatement {
	comments, code := leadingFractchComments(text)
	statement := fractchStatement{Text: text}
	if code == "" {
		statement.Kind = "note"
		statement.Key = text
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
		attributes := fractchAttributes(tokens)
		if id, ok := attributes["id"]; ok {
			statement.Key = "watch id " + unquoteFractch(id)
		}
		statement.Body = code
	case "sprite", "stage", "costume", "backdrop", "sound", "use", "platform", "comment":
		tokens := fractchTokens(code)
		if len(tokens) > 1 && isFractchQuote(tokens[1][0]) {
			statement.Name = unquoteFractch(tokens[1])
		}
		statement.Key = statement.Kind + " " + statement.Name
		if statement.Kind == "stage" {
			statement.Key = "stage"
			statement.Name = "Stage"
		}
		if statement.Kind == "backdrop" {
			statement.Key = "costume " + statement.Name
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

func parseFractchFile(src string) []fractchStatement {
	raw := splitFractchStatements(src)
	statements := make([]fractchStatement, 0, len(raw))
	for _, text := range raw {
		statements = append(statements, parseFractchStatement(text))
	}
	return statements
}

func clipStateValue(value string) (string, bool) {
	runes := []rune(value)
	if len(runes) <= maxStateValueChars {
		return value, false
	}
	return string(runes[:maxStateValueChars]), true
}

func (changes *fractchFileChanges) addState(kind, name, field, before, after string) {
	clippedBefore, beforeCut := clipStateValue(before)
	clippedAfter, afterCut := clipStateValue(after)
	changes.State = append(changes.State, fractchStateChange{
		Kind: kind, Name: name, Field: field, Before: clippedBefore, After: clippedAfter, Truncated: beforeCut || afterCut,
	})
}

func variableValue(code string) string {
	equals := -1
	for i := 0; i < len(code); {
		if isFractchQuote(code[i]) {
			i = skipFractchString(code, i)
			continue
		}
		if code[i] == '=' {
			equals = i
			break
		}
		i++
	}
	if equals < 0 {
		return ""
	}
	value := code[equals+1:]
	if match := variableIDSuffix.FindStringIndex(value); match != nil {
		value = value[:match[0]]
	}
	return strings.TrimSuffix(strings.TrimSpace(value), ";")
}

func variableKind(statement fractchStatement) string {
	if strings.HasPrefix(variableValue(statement.Body), "[") {
		return "list"
	}
	if statement.Kind == "cloud" {
		return "cloud"
	}
	return "variable"
}

var targetStateFields = map[string]bool{"at": true, "size": true, "direction": true, "hidden": true, "layer": true, "volume": true, "tempo": true, "video": true, "transparency": true, "tts": true}

func headerAttributes(statement fractchStatement) map[string]string {
	tokens := fractchTokens(statement.Body)
	if len(tokens) == 0 {
		return map[string]string{}
	}
	tokens = tokens[1:]
	if statement.Kind == "sprite" && len(tokens) > 0 && isFractchQuote(tokens[0][0]) {
		tokens = tokens[1:]
	}
	return fractchAttributes(tokens)
}

func sortedUnion(a, b map[string]string) []string {
	keys := []string{}
	for key := range a {
		keys = append(keys, key)
	}
	for key := range b {
		if _, ok := a[key]; !ok {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

var targetDefaults = map[string]string{"at": "0,0", "size": "100", "direction": "90", "volume": "100", "tempo": "60", "video": "on", "transparency": "50"}

func attributeValue(attributes map[string]string, key string) string {
	if value, ok := attributes[key]; ok {
		return unquoteFractch(value)
	}
	return targetDefaults[key]
}

func (changes *fractchFileChanges) compareHeader(before, after fractchStatement) {
	if before.Name != after.Name {
		changes.Items = append(changes.Items, fractchItemChange{Change: "renamed", Kind: after.Kind, Name: after.Name, Before: before.Name, After: after.Name})
	}
	beforeAttributes := headerAttributes(before)
	afterAttributes := headerAttributes(after)
	authored := false
	for _, key := range sortedUnion(beforeAttributes, afterAttributes) {
		if beforeAttributes[key] == afterAttributes[key] {
			continue
		}
		if targetStateFields[key] {
			field := key
			if key == "at" {
				field = "position"
			} else if key == "hidden" {
				field = "visible"
			}
			beforeValue, afterValue := attributeValue(beforeAttributes, key), attributeValue(afterAttributes, key)
			if key == "hidden" {
				beforeValue, afterValue = visibleText(beforeAttributes[key]), visibleText(afterAttributes[key])
			}
			changes.addState(after.Kind, after.Name, field, beforeValue, afterValue)
			continue
		}
		authored = true
	}
	if authored {
		changes.Items = append(changes.Items, fractchItemChange{Change: "changed", Kind: after.Kind, Name: after.Name, Before: before.Body, After: after.Body})
	}
}

func visibleText(hidden string) string {
	if hidden == "true" {
		return "false"
	}
	return "true"
}

func withoutAttributes(statement fractchStatement, names ...string) string {
	tokens := fractchTokens(statement.Body)
	kept := []string{}
	for i := 0; i < len(tokens); i++ {
		dropped := false
		for _, name := range names {
			if tokens[i] != name {
				continue
			}
			dropped = true
			if !flagAttributes[name] && i+1 < len(tokens) {
				i++
			}
		}
		if !dropped {
			kept = append(kept, tokens[i])
		}
	}
	return strings.Join(kept, " ")
}

func (changes *fractchFileChanges) compareKeyed(before, after fractchStatement) {
	if before.Body == after.Body {
		if (after.Kind == "costume" || after.Kind == "backdrop" || after.Kind == "sound") && changes.assetEdited(before, after) {
			changes.Items = append(changes.Items, fractchItemChange{Change: "edited", Kind: after.Kind, Name: after.Name, Before: before.Body, After: after.Body})
		}
		return
	}
	switch after.Kind {
	case "sprite", "stage":
		changes.compareHeader(before, after)
	case "var", "cloud":
		if before.Name != after.Name {
			changes.Items = append(changes.Items, fractchItemChange{Change: "renamed", Kind: variableKind(after), Name: after.Name, Before: before.Name, After: after.Name})
		}
		if beforeValue, afterValue := variableValue(before.Body), variableValue(after.Body); beforeValue != afterValue {
			changes.addState(variableKind(after), after.Name, "value", beforeValue, afterValue)
		}
	case "watch":
		changes.addState("monitor", after.Name, "display", before.Body, after.Body)
	case "costume", "backdrop", "sound":
		beforeCurrent := headerFlag(before, "current")
		afterCurrent := headerFlag(after, "current")
		if beforeCurrent != afterCurrent {
			changes.addState(after.Kind, after.Name, "current", beforeCurrent, afterCurrent)
		}
		if assetSignature(before) != assetSignature(after) {
			changes.Items = append(changes.Items, fractchItemChange{Change: "changed", Kind: after.Kind, Name: after.Name, Before: before.Body, After: after.Body})
		} else if changes.assetEdited(before, after) {
			changes.Items = append(changes.Items, fractchItemChange{Change: "edited", Kind: after.Kind, Name: after.Name, Before: before.Body, After: after.Body})
		}
	case "comment":
		if withoutAttributes(before, "at", "size", "minimized", "for") == withoutAttributes(after, "at", "size", "minimized", "for") {
			changes.addState("comment", after.Name, "layout", before.Body, after.Body)
			return
		}
		changes.Items = append(changes.Items, fractchItemChange{Change: "changed", Kind: "comment", Name: after.Name, Before: before.Body, After: after.Body})
	default:
		changes.Items = append(changes.Items, fractchItemChange{Change: "changed", Kind: after.Kind, Name: after.Name, Before: before.Body, After: after.Body})
	}
}

func assetFile(statement fractchStatement) string {
	return unquoteFractch(fractchAttributes(fractchTokens(statement.Body))["file"])
}

func (changes *fractchFileChanges) assetEdited(before, after fractchStatement) bool {
	beforeFile, afterFile := assetFile(before), assetFile(after)
	if beforeFile == "" || afterFile == "" {
		return false
	}
	beforeOID, afterOID := changes.assets.before(beforeFile), changes.assets.after(afterFile)
	return beforeOID != "" && afterOID != "" && beforeOID != afterOID
}

func (changes *fractchFileChanges) sameAsset(before, after fractchStatement) bool {
	beforeFile, afterFile := assetFile(before), assetFile(after)
	if beforeFile == "" || afterFile == "" {
		return false
	}
	beforeOID, afterOID := changes.assets.before(beforeFile), changes.assets.after(afterFile)
	return beforeOID != "" && beforeOID == afterOID
}

func roundedCenter(value string) string {
	parts := strings.Split(value, ",")
	for index, part := range parts {
		number, err := strconv.ParseFloat(strings.TrimSpace(part), 64)
		if err != nil {
			return value
		}
		parts[index] = strconv.FormatFloat(math.Round(number*100)/100, 'f', -1, 64)
	}
	return strings.Join(parts, ",")
}

func assetSignature(statement fractchStatement) string {
	tokens := fractchTokens(statement.Body)
	kept := []string{}
	for i := 0; i < len(tokens); i++ {
		switch tokens[i] {
		case "current":
			continue
		case "file":
			i++
			continue
		case "center":
			if i+1 < len(tokens) {
				kept = append(kept, "center", roundedCenter(tokens[i+1]))
				i++
			}
			continue
		}
		kept = append(kept, tokens[i])
	}
	return strings.Join(kept, " ")
}

func headerFlag(statement fractchStatement, flag string) string {
	for _, token := range fractchTokens(statement.Body) {
		if token == flag {
			return "true"
		}
	}
	return "false"
}

func itemKind(statement fractchStatement) string {
	if statement.Kind == "var" || statement.Kind == "cloud" {
		return variableKind(statement)
	}
	return statement.Kind
}

func (changes *fractchFileChanges) addItem(change string, statement fractchStatement) {
	item := fractchItemChange{Change: change, Kind: itemKind(statement), Name: statement.Name}
	if change == "removed" {
		item.Before = statement.Body
	} else {
		item.After = statement.Body
	}
	if statement.Kind == "watch" {
		field := "shown"
		if change == "removed" {
			field = "removed"
		}
		changes.addState("monitor", statement.Name, field, item.Before, item.After)
		return
	}
	changes.Items = append(changes.Items, item)
}

func lineSimilarity(a, b string) float64 {
	aLines := strings.Split(a, "\n")
	bLines := strings.Split(b, "\n")
	counts := make(map[string]int, len(aLines))
	for _, line := range aLines {
		counts[strings.TrimSpace(line)]++
	}
	common := 0
	for _, line := range bLines {
		key := strings.TrimSpace(line)
		if counts[key] > 0 {
			counts[key]--
			common++
		}
	}
	longest := len(aLines)
	if len(bLines) > longest {
		longest = len(bLines)
	}
	if longest == 0 {
		return 1
	}
	return float64(common) / float64(longest)
}

type scriptPair struct {
	before int
	after  int
	score  float64
}

func pairBySimilarity(before, after []fractchStatement, beforeUsed, afterUsed []bool, candidates func(i, j int) bool, minimum float64) []scriptPair {
	pairs := []scriptPair{}
	for i := range before {
		if beforeUsed[i] {
			continue
		}
		for j := range after {
			if afterUsed[j] || !candidates(i, j) {
				continue
			}
			score := lineSimilarity(before[i].Norm, after[j].Norm)
			if score >= minimum {
				pairs = append(pairs, scriptPair{before: i, after: j, score: score})
			}
		}
	}
	sort.SliceStable(pairs, func(a, b int) bool { return pairs[a].score > pairs[b].score })
	chosen := []scriptPair{}
	for _, pair := range pairs {
		if beforeUsed[pair.before] || afterUsed[pair.after] {
			continue
		}
		beforeUsed[pair.before] = true
		afterUsed[pair.after] = true
		chosen = append(chosen, pair)
	}
	return chosen
}

func (changes *fractchFileChanges) compareScripts(before, after []fractchStatement) {
	beforeUsed := make([]bool, len(before))
	afterUsed := make([]bool, len(after))
	partner := make([]int, len(after))
	for j := range partner {
		partner[j] = -1
	}
	byBody := map[string][]int{}
	for i, statement := range before {
		byBody[statement.Norm] = append(byBody[statement.Norm], i)
	}
	for j, statement := range after {
		queue := byBody[statement.Norm]
		if len(queue) == 0 {
			continue
		}
		i := queue[0]
		byBody[statement.Norm] = queue[1:]
		beforeUsed[i] = true
		afterUsed[j] = true
		if before[i].At != statement.At {
			changes.addState("script", statement.Head, "position", before[i].At, statement.At)
		}
	}
	sameKey := func(i, j int) bool { return before[i].Key == after[j].Key }
	for _, pair := range pairBySimilarity(before, after, beforeUsed, afterUsed, sameKey, 0) {
		partner[pair.after] = pair.before
	}
	remainingBefore, remainingAfter := 0, 0
	for i := range before {
		if !beforeUsed[i] {
			remainingBefore++
		}
	}
	for j := range after {
		if !afterUsed[j] {
			remainingAfter++
		}
	}
	if remainingBefore*remainingAfter <= maxCrossPairProducts {
		anyKey := func(i, j int) bool { return true }
		for _, pair := range pairBySimilarity(before, after, beforeUsed, afterUsed, anyKey, crossPairSimilarity) {
			partner[pair.after] = pair.before
		}
	}
	for j, statement := range after {
		if i := partner[j]; i >= 0 {
			changes.Scripts = append(changes.Scripts, fractchScriptChange{
				Change: "changed", Kind: scriptKind(statement), Head: statement.Head,
				Before: before[i].Body, After: statement.Body, BeforeAt: before[i].At, AfterAt: statement.At,
			})
			continue
		}
		if !afterUsed[j] {
			changes.Scripts = append(changes.Scripts, fractchScriptChange{
				Change: "added", Kind: scriptKind(statement), Head: statement.Head, After: statement.Body, AfterAt: statement.At,
			})
		}
	}
	for i, statement := range before {
		if !beforeUsed[i] {
			changes.Scripts = append(changes.Scripts, fractchScriptChange{
				Change: "removed", Kind: scriptKind(statement), Head: statement.Head, Before: statement.Body, BeforeAt: statement.At,
			})
		}
	}
}

func scriptKind(statement fractchStatement) string {
	return firstWord.FindString(statement.Head)
}

func (changes *fractchFileChanges) compareItems(before, after []fractchStatement) {
	queues := map[string][]int{}
	for i, statement := range before {
		queues[statement.Key] = append(queues[statement.Key], i)
	}
	beforeUsed := make([]bool, len(before))
	unmatched := []fractchStatement{}
	for _, statement := range after {
		queue := queues[statement.Key]
		if len(queue) == 0 {
			unmatched = append(unmatched, statement)
			continue
		}
		i := queue[0]
		queues[statement.Key] = queue[1:]
		beforeUsed[i] = true
		changes.compareKeyed(before[i], statement)
	}
	removed := []fractchStatement{}
	for i, statement := range before {
		if !beforeUsed[i] {
			removed = append(removed, statement)
		}
	}
	emptyStage := fractchStatement{Kind: "stage", Key: "stage", Name: "Stage", Body: "stage"}
	for _, statement := range unmatched {
		if statement.Kind == "stage" {
			changes.compareHeader(emptyStage, statement)
			continue
		}
		renamedFrom := -1
		if statement.Kind == "costume" || statement.Kind == "backdrop" || statement.Kind == "sound" || statement.Kind == "sprite" {
			for index, old := range removed {
				if old.Kind == statement.Kind && (changes.sameAsset(old, statement) || (assetFile(old) != "" && assetFile(old) == assetFile(statement)) || (statement.Kind == "sprite" && withoutName(old) == withoutName(statement))) {
					renamedFrom = index
					break
				}
			}
		}
		if renamedFrom >= 0 {
			old := removed[renamedFrom]
			removed = append(removed[:renamedFrom], removed[renamedFrom+1:]...)
			if statement.Kind == "sprite" {
				changes.compareHeader(old, statement)
			} else {
				changes.Items = append(changes.Items, fractchItemChange{Change: "renamed", Kind: statement.Kind, Name: statement.Name, Before: old.Name, After: statement.Name})
			}
			continue
		}
		changes.addItem("added", statement)
	}
	for _, statement := range removed {
		if statement.Kind == "stage" {
			changes.compareHeader(statement, emptyStage)
			continue
		}
		changes.addItem("removed", statement)
	}
}

func withoutName(statement fractchStatement) string {
	tokens := fractchTokens(statement.Body)
	if len(tokens) > 1 && isFractchQuote(tokens[1][0]) {
		tokens = append([]string{tokens[0]}, tokens[2:]...)
	}
	return strings.Join(tokens, " ")
}

func compareFractch(before, after string) fractchFileChanges {
	return compareFractchAssets(before, after, assetLookup{before: noAssets, after: noAssets})
}

func compareFractchAssets(before, after string, assets assetLookup) fractchFileChanges {
	changes := fractchFileChanges{Scripts: []fractchScriptChange{}, Items: []fractchItemChange{}, State: []fractchStateChange{}, assets: assets}
	beforeScripts, beforeItems := partitionStatements(parseFractchFile(before))
	afterScripts, afterItems := partitionStatements(parseFractchFile(after))
	changes.compareItems(beforeItems, afterItems)
	changes.compareScripts(beforeScripts, afterScripts)
	return changes
}

func partitionStatements(statements []fractchStatement) ([]fractchStatement, []fractchStatement) {
	scripts := []fractchStatement{}
	items := []fractchStatement{}
	for _, statement := range statements {
		if statement.Kind == "script" {
			scripts = append(scripts, statement)
		} else {
			items = append(items, statement)
		}
	}
	return scripts, items
}
