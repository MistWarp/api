package gitinspection

import (
	"encoding/hex"
	"sort"
	"strings"
	"testing"
)

const fractchBase = `/**
 * target: Cat
 * targetId: abc
 */

sprite "Cat" at 10,20 layer 2;
var score = 0 id "v1";
var items = [1,2] id "l1";
watch var "score" at 5,5;
costume "Idle" file "assets/Idle.svg" center 48,50 current;
costume "Run" file "assets/Run.svg" center 48,50;
sound "Meow" file "assets/Meow.wav";

when flag at 0,0 {
  score = 0;
  say "hi {there}; // not a comment";
}

when flag at 300,0 {
  forever {
    turn 15;
  }
}

// helper
def @jump(height) warp at 0,400 {
  changeY height;
}
`

func scriptChanges(t *testing.T, changes fractchFileChanges, change string) []fractchScriptChange {
	t.Helper()
	found := []fractchScriptChange{}
	for _, script := range changes.Scripts {
		if script.Change == change {
			found = append(found, script)
		}
	}
	return found
}

func stateFields(changes fractchFileChanges) []string {
	fields := []string{}
	for _, state := range changes.State {
		fields = append(fields, state.Kind+" "+state.Name+" "+state.Field+" "+state.Before+" -> "+state.After)
	}
	sort.Strings(fields)
	return fields
}

func TestCompareFractchUnchangedFileHasNoChanges(t *testing.T) {
	changes := compareFractch(fractchBase, fractchBase)
	if len(changes.Scripts) != 0 || len(changes.Items) != 0 || len(changes.State) != 0 {
		t.Fatalf("expected no changes, got %+v", changes)
	}
}

func TestCompareFractchSeparatesSavedStateFromCode(t *testing.T) {
	after := strings.NewReplacer(
		`sprite "Cat" at 10,20 layer 2;`, `sprite "Cat" at -4,7 hidden layer 5;`,
		`var score = 0 id "v1";`, `var score = 12 id "v1";`,
		`watch var "score" at 5,5;`, `watch var "score" at 5,40 hidden;`,
		`center 48,50 current;`, `center 48,50;`,
		`file "assets/Run.svg" center 48,50;`, `file "assets/Run.svg" center 48,50 current;`,
		`when flag at 300,0 {`, `when flag at 512,96 {`,
	).Replace(fractchBase)
	changes := compareFractch(fractchBase, after)
	if len(changes.Scripts) != 0 || len(changes.Items) != 0 {
		t.Fatalf("saved state was reported as code: scripts %+v items %+v", changes.Scripts, changes.Items)
	}
	want := []string{
		"costume Idle current true -> false",
		"costume Run current false -> true",
		`monitor var score display watch var "score" at 5,5; -> watch var "score" at 5,40 hidden;`,
		"script when flag position 300,0 -> 512,96",
		"sprite Cat layer 2 -> 5",
		"sprite Cat position 10,20 -> -4,7",
		"sprite Cat visible true -> false",
		"variable score value 0 -> 12",
	}
	got := stateFields(changes)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("state changes:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestCompareFractchPairsEditedScriptAmongSameHats(t *testing.T) {
	after := strings.Replace(fractchBase, "turn 15;", "turn 30;", 1)
	changes := compareFractch(fractchBase, after)
	changed := scriptChanges(t, changes, "changed")
	if len(changes.Scripts) != 1 || len(changed) != 1 {
		t.Fatalf("expected one changed script, got %+v", changes.Scripts)
	}
	if !strings.Contains(changed[0].Before, "turn 15") || !strings.Contains(changed[0].After, "turn 30") || changed[0].Head != "when flag" {
		t.Fatalf("wrong script pairing: %+v", changed[0])
	}
	if strings.Contains(changed[0].After, "at 300,0") {
		t.Fatalf("script text keeps its canvas position: %q", changed[0].After)
	}
}

func TestCompareFractchIgnoresScriptOrder(t *testing.T) {
	first := "when flag at 0,0 {\n  score = 0;\n  say \"hi {there}; // not a comment\";\n}"
	second := "when flag at 300,0 {\n  forever {\n    turn 15;\n  }\n}"
	reordered := strings.Replace(fractchBase, first+"\n\n"+second, second+"\n\n"+first, 1)
	if reordered == fractchBase {
		t.Fatal("test setup did not reorder scripts")
	}
	changes := compareFractch(fractchBase, reordered)
	if len(changes.Scripts) != 0 || len(changes.Items) != 0 || len(changes.State) != 0 {
		t.Fatalf("reordering scripts reported changes: %+v", changes)
	}
}

func TestCompareFractchReportsAddedAndRemovedScripts(t *testing.T) {
	after := strings.Replace(fractchBase, "when flag at 300,0 {\n  forever {\n    turn 15;\n  }\n}", "when key \"space\" pressed at 0,700 {\n  jump(10);\n}", 1)
	changes := compareFractch(fractchBase, after)
	added := scriptChanges(t, changes, "added")
	removed := scriptChanges(t, changes, "removed")
	if len(added) != 1 || len(removed) != 1 || len(changes.Scripts) != 2 {
		t.Fatalf("expected one added and one removed script, got %+v", changes.Scripts)
	}
	if added[0].Head != `when key "space" pressed` || added[0].AfterAt != "0,700" || removed[0].BeforeAt != "300,0" {
		t.Fatalf("wrong scripts: %+v %+v", added[0], removed[0])
	}
}

func TestCompareFractchMatchesDefinitionsByName(t *testing.T) {
	after := strings.Replace(fractchBase, "def @jump(height) warp at 0,400 {\n  changeY height;", "def @jump(height, speed) warp at 0,400 {\n  changeY height * speed;", 1)
	changes := compareFractch(fractchBase, after)
	changed := scriptChanges(t, changes, "changed")
	if len(changes.Scripts) != 1 || len(changed) != 1 || changed[0].Kind != "def" {
		t.Fatalf("expected the definition to be changed, got %+v", changes.Scripts)
	}
	if !strings.HasPrefix(changed[0].Before, "// helper\n") {
		t.Fatalf("leading comment is not part of the script: %q", changed[0].Before)
	}
}

func TestCompareFractchReportsAssetAndVariableEdits(t *testing.T) {
	after := strings.NewReplacer(
		`costume "Run" file "assets/Run.svg"`, `costume "Sprint" file "assets/Run.svg"`,
		`sound "Meow" file "assets/Meow.wav";`, "sound \"Meow\" file \"assets/Meow.wav\";\nsound \"Purr\" file \"assets/Purr.wav\";",
		`var items = [1,2] id "l1";`, `var stock = [1,2] id "l1";`,
		`costume "Idle" file "assets/Idle.svg" center 48,50 current;`, `costume "Idle" file "assets/Idle.svg" center 10,10 current;`,
	).Replace(fractchBase)
	changes := compareFractch(fractchBase, after)
	got := []string{}
	for _, item := range changes.Items {
		got = append(got, item.Change+" "+item.Kind+" "+item.Name)
	}
	sort.Strings(got)
	want := []string{"added sound Purr", "changed costume Idle", "renamed costume Sprint", "renamed list stock"}
	if strings.Join(got, ", ") != strings.Join(want, ", ") {
		t.Fatalf("items %v, want %v", got, want)
	}
	if len(changes.State) != 0 || len(changes.Scripts) != 0 {
		t.Fatalf("unexpected changes: %+v", changes)
	}
}

func TestCompareFractchStageSettingsAreState(t *testing.T) {
	before := "/**\n * target: Stage\n * targetId: \n */\n\nuse \"pen\";\nvar x = 1 id \"a\";\n"
	after := "/**\n * target: Stage\n * targetId: \n */\n\nstage volume 40 tempo 90;\nuse \"pen\";\nvar x = 1 id \"a\";\n"
	changes := compareFractch(before, after)
	got := stateFields(changes)
	want := []string{"stage Stage tempo 60 -> 90", "stage Stage volume 100 -> 40"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") || len(changes.Items) != 0 {
		t.Fatalf("stage changes %v items %+v", got, changes.Items)
	}
}

func TestSplitFractchStatementsSkipsStringsAndComments(t *testing.T) {
	src := "var a = \"x;y{\" id \"1\";\nwhen flag at 1,2 {\n  say \"\"\"raw } text\n{\"\"\";\n  // brace } in comment\n  say `tick } ;`;\n}\n/* stray */\n"
	statements := splitFractchStatements(src)
	if len(statements) != 3 {
		t.Fatalf("expected three statements, got %d: %q", len(statements), statements)
	}
	if !strings.HasSuffix(statements[1], "say `tick } ;`;\n}") {
		t.Fatalf("script cut short: %q", statements[1])
	}
	if statements[2] != "/* stray */" {
		t.Fatalf("trailing comment lost: %q", statements[2])
	}
}

func TestCompareFractchClipsLongStateValues(t *testing.T) {
	long := "[" + strings.Repeat("1.2345,", 200) + "0]"
	before := "sprite \"A\";\nvar y = [] id \"l\";\n"
	after := "sprite \"A\";\nvar y = " + long + " id \"l\";\n"
	changes := compareFractch(before, after)
	if len(changes.State) != 1 || !changes.State[0].Truncated || len([]rune(changes.State[0].After)) != maxStateValueChars || changes.State[0].Kind != "list" {
		t.Fatalf("long list value not clipped: %+v", changes.State)
	}
}

func (r *testRepository) nestedTree(files map[string]string) string {
	type entry struct {
		name string
		mode string
		oid  string
	}
	children := map[string]map[string]string{}
	entries := []entry{}
	for path, oid := range files {
		if slash := strings.IndexByte(path, '/'); slash >= 0 {
			dir := path[:slash]
			if children[dir] == nil {
				children[dir] = map[string]string{}
			}
			children[dir][path[slash+1:]] = oid
			continue
		}
		entries = append(entries, entry{name: path, mode: "100644", oid: oid})
	}
	for dir, nested := range children {
		entries = append(entries, entry{name: dir, mode: "40000", oid: r.nestedTree(nested)})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
	body := []byte{}
	for _, item := range entries {
		decoded, _ := hex.DecodeString(item.oid)
		body = append(body, []byte(item.mode+" "+item.name+"\x00")...)
		body = append(body, decoded...)
	}
	return r.object("tree", body)
}

func TestInspectChangesSummarisesCommit(t *testing.T) {
	repo := newTestRepository()
	asset := repo.object("blob", []byte("<svg/>"))
	firstTree := repo.nestedTree(map[string]string{
		"Cat/main.fractch":     repo.object("blob", []byte(fractchBase)),
		"Cat/assets/Idle.svg":  asset,
		"Stage/main.fractch":   repo.object("blob", []byte("var x = 1 id \"a\";\n")),
		"fractch.json":         repo.object("blob", []byte(`{"order":["Cat"]}`)),
		"Cat/assets/Run.svg":   asset,
		"Cat/assets/Meow.wav":  repo.object("blob", []byte("RIFF")),
		"Stage/assets/bg.svg":  asset,
		"README.md":            repo.object("blob", []byte("hello\n")),
		"Stage/assets/old.svg": asset,
	})
	first := repo.commit(firstTree, "", "Initial version")
	edited := strings.Replace(fractchBase, "turn 15;", "turn 45;", 1)
	secondTree := repo.nestedTree(map[string]string{
		"Cat/main.fractch":     repo.object("blob", []byte(edited)),
		"Cat/assets/Idle.svg":  asset,
		"Stage/main.fractch":   repo.object("blob", []byte("var x = 2 id \"a\";\n")),
		"fractch.json":         repo.object("blob", []byte(`{"order":["Cat","Dog"]}`)),
		"Cat/assets/Run.svg":   asset,
		"Cat/assets/Meow.wav":  repo.object("blob", []byte("RIFF2")),
		"Stage/assets/bg.svg":  asset,
		"README.md":            repo.object("blob", []byte("hello\nworld\n")),
		"Stage/assets/new.svg": repo.object("blob", []byte("<svg id=\"n\"/>")),
	})
	second := repo.commit(secondTree, first, "Updated project")
	path := repo.write(t)

	result := decodeResult(t, Inspect(path, second, "changes", "", second))
	if result["ok"] != true {
		t.Fatalf("inspection failed: %v", result)
	}
	totals := result["totals"].(map[string]any)
	scripts := totals["scripts"].(map[string]any)
	assets := totals["assets"].(map[string]any)
	if scripts["changed"] != float64(1) || scripts["added"] != float64(0) || totals["state"] != float64(1) || totals["codeChanged"] != true {
		t.Fatalf("unexpected totals: %v", totals)
	}
	if assets["added"] != float64(1) || assets["removed"] != float64(1) || assets["modified"] != float64(1) || totals["files"] != float64(2) {
		t.Fatalf("unexpected asset totals: %v", totals)
	}
	targets := result["targets"].([]any)
	if len(targets) != 2 || targets[0].(map[string]any)["path"] != "Cat/main.fractch" || targets[1].(map[string]any)["path"] != "Stage/main.fractch" {
		t.Fatalf("unexpected targets: %v", targets)
	}
	for _, raw := range result["files"].([]any) {
		file := raw.(map[string]any)
		if file["path"] == "README.md" && file["newData"] == nil {
			t.Fatalf("readme text was not inlined: %v", file)
		}
		if isFractchPath(file["path"].(string)) {
			t.Fatalf("fractch file listed with plain files: %v", file)
		}
	}

	initial := decodeResult(t, Inspect(path, first, "changes", "", second))
	initialTotals := initial["totals"].(map[string]any)
	if initialTotals["scripts"].(map[string]any)["added"] != float64(3) || initialTotals["codeChanged"] != true {
		t.Fatalf("initial commit totals: %v", initialTotals)
	}
}

func TestInspectChangesStateOnlyCommitIsNotCode(t *testing.T) {
	repo := newTestRepository()
	firstTree := repo.nestedTree(map[string]string{"Cat/main.fractch": repo.object("blob", []byte(fractchBase))})
	first := repo.commit(firstTree, "", "Initial version")
	moved := strings.Replace(fractchBase, `sprite "Cat" at 10,20`, `sprite "Cat" at 99,20`, 1)
	secondTree := repo.nestedTree(map[string]string{"Cat/main.fractch": repo.object("blob", []byte(moved))})
	second := repo.commit(secondTree, first, "Updated project")
	result := decodeResult(t, Inspect(repo.write(t), second, "changes", "", second))
	totals := result["totals"].(map[string]any)
	if totals["codeChanged"] != false || totals["state"] != float64(1) {
		t.Fatalf("state-only commit totals: %v", totals)
	}
}

func TestCompareArchivesChangesAcrossForks(t *testing.T) {
	base := newTestRepository()
	baseCommit := base.commit(base.nestedTree(map[string]string{"Cat/main.fractch": base.object("blob", []byte(fractchBase))}), "", "Initial version")
	head := newTestRepository()
	headCommit := head.commit(head.nestedTree(map[string]string{"Cat/main.fractch": head.object("blob", []byte(strings.Replace(fractchBase, "score = 0;", "score = 5;", 1)))}), "", "Remix")
	result := decodeResult(t, CompareArchivesChangesJSON(base.write(t), baseCommit, head.write(t), headCommit))
	if result["ok"] != true {
		t.Fatalf("compare failed: %v", result)
	}
	scripts := result["totals"].(map[string]any)["scripts"].(map[string]any)
	if scripts["changed"] != float64(1) {
		t.Fatalf("unexpected totals: %v", result["totals"])
	}
}

func TestCompareFractchIgnoresHiddenValuesUnderReporters(t *testing.T) {
	before := "sprite \"A\";\nwhen flag at 0,0 {\n  setX clamp(xPosition(), -100, 100);\n  say answer();\n  point (x * 2) + 1;\n}\n"
	after := "sprite \"A\";\nwhen flag at 0,0 {\n  setX (clamp(xPosition(), -100, 100) ?? -100);\n  say (answer() ?? \"Hello (there)\");\n  point (x * 2) + 1;\n}\n"
	changes := compareFractch(before, after)
	if len(changes.Scripts) != 0 {
		t.Fatalf("hidden values were reported as edits: %+v", changes.Scripts)
	}
	edited := strings.Replace(after, "say (answer()", "think (answer()", 1)
	if changed := compareFractch(before, edited); len(changed.Scripts) != 1 || changed.Scripts[0].Change != "changed" {
		t.Fatalf("a real edit next to a hidden value was lost: %+v", changed.Scripts)
	}
}

func TestStripHiddenValuesKeepsCallsAndExpressions(t *testing.T) {
	cases := map[string]string{
		"setX (x ?? -30);":            "setX x;",
		"say (join(a, b) ?? \"hi\");": "say join(a, b);",
		"go((x ?? 1));":               "go(x);",
		"f(x ?? 1);":                  "f(x);",
		"move (a + b ?? 3) * 2;":      "move (a + b) * 2;",
		"say \"(x ?? 1)\";":           "say \"(x ?? 1)\";",
		"say (answer() ?? (\"a\"));":  "say answer();",
		"pick(a ?? 1, b);":            "pick(a ?? 1, b);",
	}
	for input, want := range cases {
		if got := stripHiddenValues(input); got != want {
			t.Errorf("stripHiddenValues(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestCompareFractchIgnoresRotationCentreJitter(t *testing.T) {
	before := "sprite \"A\";\ncostume \"Sign\" file \"assets/Sign.svg\" center 80.86252757812503,132.723585 current;\n"
	after := "sprite \"A\";\ncostume \"Sign\" file \"assets/Sign.svg\" center 80.862527578125,132.72357500000004 current;\n"
	if changes := compareFractch(before, after); len(changes.Items) != 0 || len(changes.State) != 0 {
		t.Fatalf("float jitter in a rotation centre was reported: %+v", changes)
	}
	moved := strings.Replace(after, "center 80.862527578125", "center 40", 1)
	if changes := compareFractch(before, moved); len(changes.Items) != 1 || changes.Items[0].Change != "changed" {
		t.Fatalf("a moved rotation centre was not reported: %+v", changes.Items)
	}
}

func TestCompareFractchMatchesCostumesByAssetContent(t *testing.T) {
	before := "sprite \"A\";\ncostume \"Magnibrowse\" file \"assets/Magnibrowse.svg\" center 10,10;\ncostume \"Idle\" file \"assets/Idle.svg\" center 10,10;\n"
	after := "sprite \"A\";\ncostume \"iframe browse\" file \"assets/iframe_browse.svg\" center 10,10;\ncostume \"Idle\" file \"assets/Idle.svg\" center 10,10;\n"
	beforeAssets := map[string]string{"assets/Magnibrowse.svg": "aaa", "assets/Idle.svg": "bbb"}
	afterAssets := map[string]string{"assets/iframe_browse.svg": "aaa", "assets/Idle.svg": "ccc"}
	changes := compareFractchAssets(before, after, assetLookup{
		before: func(path string) string { return beforeAssets[path] },
		after:  func(path string) string { return afterAssets[path] },
	})
	got := []string{}
	for _, item := range changes.Items {
		got = append(got, item.Change+" "+item.Name)
	}
	sort.Strings(got)
	if strings.Join(got, ", ") != "edited Idle, renamed iframe browse" {
		t.Fatalf("items %v", got)
	}
}

func TestCompareFractchIgnoresOlderSnakeCaseSpellings(t *testing.T) {
	before := "sprite \"A\";\nwhen flag at 0,0 {\n  wait_until !mouseDown();\n  if sprites[\"_stage_\"].backdrop_number == 3 {\n    stop other_scripts_in_sprite;\n  }\n}\n"
	after := "sprite \"A\";\nwhen flag at 0,0 {\n  waitUntil !mouseDown();\n  if sprites[\"_stage_\"].backdropNumber == 3 {\n    stop otherScriptsInSprite;\n  }\n}\n"
	if changes := compareFractch(before, after); len(changes.Scripts) != 0 {
		t.Fatalf("a newer fractch spelling was reported as an edit: %+v", changes.Scripts)
	}
	renamedString := strings.Replace(after, `sprites["_stage_"]`, `sprites["_stage"]`, 1)
	if changes := compareFractch(before, renamedString); len(changes.Scripts) != 1 {
		t.Fatalf("an edit inside a string was hidden: %+v", changes.Scripts)
	}
}

func TestCamelIdentifiers(t *testing.T) {
	cases := map[string]string{
		"wait_until x_pos;":        "waitUntil xPos;",
		"Big_Name _private a1_b2":  "Big_Name _private a1B2",
		"say \"snake_case\";":      "say \"snake_case\";",
		"// keep_this\nset_x 1;":   "// keep_this\nsetX 1;",
		"strings.menu_textCase(x)": "strings.menuTextCase(x)",
	}
	for input, want := range cases {
		if got := camelIdentifiers(input); got != want {
			t.Errorf("camelIdentifiers(%q) = %q, want %q", input, got, want)
		}
	}
}
