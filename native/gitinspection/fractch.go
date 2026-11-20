package gitinspection

import (
	"math"
	"sort"
	"strconv"
	"strings"
)

const (
	maxStateValueChars  = 160
	maxPairsPerGroup    = 10_000
	maxSimilarityWork   = 2_000_000
	crossPairSimilarity = 0.6
)

type fractchScriptChange struct {
	Change     string `json:"change"`
	Kind       string `json:"kind"`
	Head       string `json:"head"`
	File       string `json:"file"`
	BeforeFile string `json:"beforeFile,omitempty"`
	Before     string `json:"before,omitempty"`
	After      string `json:"after,omitempty"`
	BeforeAt   string `json:"beforeAt,omitempty"`
	AfterAt    string `json:"afterAt,omitempty"`
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
	Name    string                `json:"-"`
	assets  assetLookup
	work    int
}

type assetLookup struct {
	before func(string) string
	after  func(string) string
}

type fractchSource struct {
	Path string
	Text string
}

func noAssets(string) string {
	return ""
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

func (changes *fractchFileChanges) addItem(change, kind, name, before, after string) {
	changes.Items = append(changes.Items, fractchItemChange{Change: change, Kind: kind, Name: name, Before: before, After: after})
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

func itemKind(statement fractchStatement) string {
	if statement.Kind == "var" || statement.Kind == "cloud" {
		if strings.HasPrefix(variableValue(statement.Body), "[") {
			return "list"
		}
		if statement.Kind == "cloud" {
			return "cloud"
		}
		return "variable"
	}
	return statement.Kind
}

var targetStateFields = map[string]string{
	"at": "position", "size": "size", "direction": "direction", "hidden": "visible", "layer": "layer", "costume": "costume",
	"volume": "volume", "tempo": "tempo", "video": "video", "transparency": "transparency", "tts": "tts",
}

var targetDefaults = map[string]string{"at": "0,0", "size": "100", "direction": "90", "volume": "100", "tempo": "60", "video": "on", "transparency": "50", "hidden": "true"}

func headerAttributes(statement fractchStatement) map[string]string {
	tokens := fractchTokens(statement.Body)
	if len(tokens) == 0 {
		return map[string]string{}
	}
	tokens = tokens[1:]
	if statement.Kind == "sprite" && len(tokens) > 0 && isFractchQuote(tokens[0][0]) {
		tokens = tokens[1:]
	}
	attributes := fractchAttributes(tokens)
	delete(attributes, "visible")
	if _, hidden := attributes["hidden"]; hidden {
		attributes["hidden"] = "false"
	}
	return attributes
}

func attributeValue(attributes map[string]string, key string) string {
	if value, ok := attributes[key]; ok {
		return unquoteFractch(value)
	}
	return targetDefaults[key]
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

func (changes *fractchFileChanges) compareHeader(before, after fractchStatement) {
	if before.Name != after.Name {
		changes.addItem("renamed", after.Kind, after.Name, before.Name, after.Name)
	}
	beforeAttributes := headerAttributes(before)
	afterAttributes := headerAttributes(after)
	authored := false
	for _, key := range sortedUnion(beforeAttributes, afterAttributes) {
		beforeValue, afterValue := attributeValue(beforeAttributes, key), attributeValue(afterAttributes, key)
		if beforeValue == afterValue {
			continue
		}
		if field, ok := targetStateFields[key]; ok {
			changes.addState(after.Kind, after.Name, field, beforeValue, afterValue)
			continue
		}
		authored = true
	}
	if authored {
		changes.addItem("changed", after.Kind, after.Name, before.Body, after.Body)
	}
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

func headerFlag(statement fractchStatement, flag string) string {
	for _, token := range fractchTokens(statement.Body) {
		if token == flag {
			return "true"
		}
	}
	return "false"
}

func assetFile(statement fractchStatement) string {
	return unquoteFractch(fractchAttributes(fractchTokens(statement.Body))["file"])
}

func isAssetKind(kind string) bool {
	return kind == "costume" || kind == "backdrop" || kind == "sound"
}

func (changes *fractchFileChanges) assetOIDs(before, after fractchStatement) (string, string) {
	beforeFile, afterFile := assetFile(before), assetFile(after)
	if beforeFile == "" || afterFile == "" {
		return "", ""
	}
	return changes.assets.before(beforeFile), changes.assets.after(afterFile)
}

func (changes *fractchFileChanges) assetEdited(before, after fractchStatement) bool {
	beforeOID, afterOID := changes.assetOIDs(before, after)
	return beforeOID != "" && afterOID != "" && beforeOID != afterOID
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

func (changes *fractchFileChanges) compareKeyed(before, after fractchStatement) {
	if before.Body == after.Body {
		if isAssetKind(after.Kind) && changes.assetEdited(before, after) {
			changes.addItem("edited", after.Kind, after.Name, before.Body, after.Body)
		}
		return
	}
	switch after.Kind {
	case "sprite", "stage":
		changes.compareHeader(before, after)
	case "var", "cloud":
		if before.Name != after.Name {
			changes.addItem("renamed", itemKind(after), after.Name, before.Name, after.Name)
		}
		if beforeValue, afterValue := variableValue(before.Body), variableValue(after.Body); beforeValue != afterValue {
			changes.addState(itemKind(after), after.Name, "value", beforeValue, afterValue)
		}
	case "watch":
		changes.addState("monitor", after.Name, "display", before.Body, after.Body)
	case "costume", "backdrop", "sound":
		if beforeCurrent, afterCurrent := headerFlag(before, "current"), headerFlag(after, "current"); beforeCurrent != afterCurrent {
			changes.addState(after.Kind, after.Name, "current", beforeCurrent, afterCurrent)
		}
		if assetSignature(before) != assetSignature(after) {
			changes.addItem("changed", after.Kind, after.Name, before.Body, after.Body)
		} else if changes.assetEdited(before, after) {
			changes.addItem("edited", after.Kind, after.Name, before.Body, after.Body)
		}
	case "comment":
		layout := []string{"at", "size", "minimized", "for"}
		if withoutAttributes(before, layout...) == withoutAttributes(after, layout...) {
			changes.addState("comment", after.Name, "layout", before.Body, after.Body)
			return
		}
		changes.addItem("changed", "comment", after.Name, before.Body, after.Body)
	default:
		changes.addItem("changed", itemKind(after), after.Name, before.Body, after.Body)
	}
}

func (changes *fractchFileChanges) addWhole(change string, statement fractchStatement) {
	before, after := "", statement.Body
	if change == "removed" {
		before, after = statement.Body, ""
	}
	if statement.Kind == "watch" {
		field := "shown"
		if change == "removed" {
			field = "removed"
		}
		changes.addState("monitor", statement.Name, field, before, after)
		return
	}
	changes.addItem(change, itemKind(statement), statement.Name, before, after)
}

func withoutName(statement fractchStatement) string {
	tokens := fractchTokens(statement.Body)
	if len(tokens) > 1 && isFractchQuote(tokens[1][0]) {
		tokens = append([]string{tokens[0]}, tokens[2:]...)
	}
	return strings.Join(tokens, " ")
}

func renameKeys(changes *fractchFileChanges, statement fractchStatement, before bool) []string {
	keys := []string{}
	if statement.Kind == "sprite" {
		return append(keys, "sprite "+withoutName(statement))
	}
	if !isAssetKind(statement.Kind) {
		return keys
	}
	file := assetFile(statement)
	if file == "" {
		return keys
	}
	keys = append(keys, statement.Kind+" file "+file)
	lookup := changes.assets.after
	if before {
		lookup = changes.assets.before
	}
	if oid := lookup(file); oid != "" {
		keys = append(keys, statement.Kind+" oid "+oid)
	}
	return keys
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
	renameIndex := map[string][]int{}
	for i, statement := range before {
		if beforeUsed[i] {
			continue
		}
		for _, key := range renameKeys(changes, statement, true) {
			renameIndex[key] = append(renameIndex[key], i)
		}
	}
	emptyStage := fractchStatement{Kind: "stage", Key: "stage", Name: "Stage", Body: "stage"}
	for _, statement := range unmatched {
		if statement.Kind == "stage" {
			changes.compareHeader(emptyStage, statement)
			continue
		}
		renamedFrom := -1
		for _, key := range renameKeys(changes, statement, false) {
			for len(renameIndex[key]) > 0 && beforeUsed[renameIndex[key][0]] {
				renameIndex[key] = renameIndex[key][1:]
			}
			if len(renameIndex[key]) > 0 {
				renamedFrom = renameIndex[key][0]
				break
			}
		}
		if renamedFrom < 0 {
			changes.addWhole("added", statement)
			continue
		}
		beforeUsed[renamedFrom] = true
		old := before[renamedFrom]
		if statement.Kind == "sprite" {
			changes.compareHeader(old, statement)
			continue
		}
		changes.addItem("renamed", statement.Kind, statement.Name, old.Name, statement.Name)
	}
	for i, statement := range before {
		if beforeUsed[i] {
			continue
		}
		if statement.Kind == "stage" {
			changes.compareHeader(statement, emptyStage)
			continue
		}
		changes.addWhole("removed", statement)
	}
	changes.compareOrder(before, after, "costume", "costume")
	changes.compareOrder(before, after, "sound", "sound")
}

func (changes *fractchFileChanges) compareOrder(before, after []fractchStatement, kind, label string) {
	matches := func(statement fractchStatement) bool {
		return statement.Kind == kind || (kind == "costume" && statement.Kind == "backdrop")
	}
	inAfter := map[string]bool{}
	for _, statement := range after {
		if matches(statement) {
			inAfter[statement.Key] = true
		}
	}
	inBoth := map[string]bool{}
	beforeOrder := []string{}
	for _, statement := range before {
		if matches(statement) && inAfter[statement.Key] && !inBoth[statement.Key] {
			inBoth[statement.Key] = true
			beforeOrder = append(beforeOrder, statement.Name)
		}
	}
	afterOrder := []string{}
	seen := map[string]bool{}
	for _, statement := range after {
		if matches(statement) && inBoth[statement.Key] && !seen[statement.Key] {
			seen[statement.Key] = true
			afterOrder = append(afterOrder, statement.Name)
		}
	}
	if strings.Join(beforeOrder, "\x00") != strings.Join(afterOrder, "\x00") {
		changes.addItem("reordered", label, "", strings.Join(beforeOrder, "\n"), strings.Join(afterOrder, "\n"))
	}
}

func statementLines(statement *fractchStatement) []string {
	if statement.lines == nil {
		raw := strings.Split(statement.Norm, "\n")
		statement.lines = make([]string, len(raw))
		for index, line := range raw {
			statement.lines[index] = strings.TrimSpace(line)
		}
	}
	return statement.lines
}

func (changes *fractchFileChanges) similarity(a, b *fractchStatement) (float64, bool) {
	aLines, bLines := statementLines(a), statementLines(b)
	cost := len(aLines) + len(bLines)
	if changes.work+cost > maxSimilarityWork {
		return 0, false
	}
	changes.work += cost
	counts := make(map[string]int, len(aLines))
	for _, line := range aLines {
		counts[line]++
	}
	common := 0
	for _, line := range bLines {
		if counts[line] > 0 {
			counts[line]--
			common++
		}
	}
	longest := len(aLines)
	if len(bLines) > longest {
		longest = len(bLines)
	}
	if longest == 0 {
		return 1, true
	}
	return float64(common) / float64(longest), true
}

type scriptPair struct {
	before int
	after  int
	score  float64
}

func (changes *fractchFileChanges) pairGroup(before, after []fractchStatement, beforeIDs, afterIDs []int, beforeUsed, afterUsed []bool, minimum float64, byOrder bool) []scriptPair {
	open := func(ids []int, used []bool) []int {
		kept := []int{}
		for _, id := range ids {
			if !used[id] {
				kept = append(kept, id)
			}
		}
		return kept
	}
	beforeIDs, afterIDs = open(beforeIDs, beforeUsed), open(afterIDs, afterUsed)
	chosen := []scriptPair{}
	if len(beforeIDs) == 0 || len(afterIDs) == 0 {
		return chosen
	}
	pairs := []scriptPair{}
	tooMany := len(beforeIDs)*len(afterIDs) > maxPairsPerGroup
	if !tooMany {
		for _, i := range beforeIDs {
			for _, j := range afterIDs {
				score, ok := changes.similarity(&before[i], &after[j])
				if !ok {
					tooMany = true
					break
				}
				if score >= minimum {
					pairs = append(pairs, scriptPair{before: i, after: j, score: score})
				}
			}
			if tooMany {
				break
			}
		}
	}
	if tooMany {
		if !byOrder {
			return chosen
		}
		pairs = pairs[:0]
		for index := 0; index < len(beforeIDs) && index < len(afterIDs); index++ {
			pairs = append(pairs, scriptPair{before: beforeIDs[index], after: afterIDs[index]})
		}
	}
	sort.SliceStable(pairs, func(a, b int) bool { return pairs[a].score > pairs[b].score })
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

func scriptKind(statement fractchStatement) string {
	return firstWord.FindString(statement.Head)
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
		if before[i].File != statement.File {
			changes.Scripts = append(changes.Scripts, fractchScriptChange{
				Change: "moved", Kind: scriptKind(statement), Head: statement.Head, File: statement.File, BeforeFile: before[i].File,
				BeforeAt: before[i].At, AfterAt: statement.At,
			})
		} else if before[i].At != statement.At {
			changes.addState("script", statement.Head, "position", before[i].At, statement.At)
		}
	}
	beforeByKey := map[string][]int{}
	afterByKey := map[string][]int{}
	keys := []string{}
	for i, statement := range before {
		if !beforeUsed[i] {
			beforeByKey[statement.Key] = append(beforeByKey[statement.Key], i)
		}
	}
	for j, statement := range after {
		if afterUsed[j] {
			continue
		}
		if _, ok := afterByKey[statement.Key]; !ok {
			keys = append(keys, statement.Key)
		}
		afterByKey[statement.Key] = append(afterByKey[statement.Key], j)
	}
	for _, key := range keys {
		for _, pair := range changes.pairGroup(before, after, beforeByKey[key], afterByKey[key], beforeUsed, afterUsed, 0, true) {
			partner[pair.after] = pair.before
		}
	}
	remainingBefore, remainingAfter := []int{}, []int{}
	for i := range before {
		if !beforeUsed[i] {
			remainingBefore = append(remainingBefore, i)
		}
	}
	for j := range after {
		if !afterUsed[j] {
			remainingAfter = append(remainingAfter, j)
		}
	}
	for _, pair := range changes.pairGroup(before, after, remainingBefore, remainingAfter, beforeUsed, afterUsed, crossPairSimilarity, false) {
		partner[pair.after] = pair.before
	}
	for j, statement := range after {
		if i := partner[j]; i >= 0 {
			change := fractchScriptChange{
				Change: "changed", Kind: scriptKind(statement), Head: statement.Head, File: statement.File,
				Before: before[i].Body, After: statement.Body, BeforeAt: before[i].At, AfterAt: statement.At,
			}
			if before[i].File != statement.File {
				change.BeforeFile = before[i].File
			}
			changes.Scripts = append(changes.Scripts, change)
			continue
		}
		if !afterUsed[j] {
			changes.Scripts = append(changes.Scripts, fractchScriptChange{
				Change: "added", Kind: scriptKind(statement), Head: statement.Head, File: statement.File, After: statement.Body, AfterAt: statement.At,
			})
		}
	}
	for i, statement := range before {
		if !beforeUsed[i] {
			changes.Scripts = append(changes.Scripts, fractchScriptChange{
				Change: "removed", Kind: scriptKind(statement), Head: statement.Head, File: statement.File, Before: statement.Body, BeforeAt: statement.At,
			})
		}
	}
}

func partitionStatements(sources []fractchSource) ([]fractchStatement, []fractchStatement) {
	scripts := []fractchStatement{}
	items := []fractchStatement{}
	for _, source := range sources {
		for _, statement := range parseFractchFile(source.Text, source.Path) {
			if statement.Kind == "script" {
				scripts = append(scripts, statement)
			} else {
				items = append(items, statement)
			}
		}
	}
	return scripts, items
}

func compareFractchSources(before, after []fractchSource, assets assetLookup) fractchFileChanges {
	changes := fractchFileChanges{Scripts: []fractchScriptChange{}, Items: []fractchItemChange{}, State: []fractchStateChange{}, assets: assets}
	beforeScripts, beforeItems := partitionStatements(before)
	afterScripts, afterItems := partitionStatements(after)
	changes.Name = targetHeaderName(afterItems)
	if changes.Name == "" {
		changes.Name = targetHeaderName(beforeItems)
	}
	changes.compareItems(beforeItems, afterItems)
	changes.compareScripts(beforeScripts, afterScripts)
	return changes
}

func targetHeaderName(items []fractchStatement) string {
	for _, statement := range items {
		if statement.Kind == "sprite" || statement.Kind == "stage" {
			return statement.Name
		}
	}
	return ""
}

func compareFractchAssets(before, after string, assets assetLookup) fractchFileChanges {
	return compareFractchSources([]fractchSource{{Path: "main.fractch", Text: before}}, []fractchSource{{Path: "main.fractch", Text: after}}, assets)
}

func compareFractch(before, after string) fractchFileChanges {
	return compareFractchAssets(before, after, assetLookup{before: noAssets, after: noAssets})
}
