package gitinspection

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func requireFast(t *testing.T, limit time.Duration, run func()) {
	t.Helper()
	start := time.Now()
	run()
	if elapsed := time.Since(start); elapsed > limit {
		t.Fatalf("took %s, limit %s", elapsed, limit)
	}
}

func TestCompareFractchManyUnmatchedCostumesIsFast(t *testing.T) {
	var before, after strings.Builder
	before.WriteString("sprite \"A\";\n")
	after.WriteString("sprite \"A\";\n")
	for i := 0; i < 20000; i++ {
		fmt.Fprintf(&before, "costume \"a%d\" file \"assets/a%d.svg\";\n", i, i)
		fmt.Fprintf(&after, "costume \"b%d\" file \"assets/b%d.svg\";\n", i, i)
	}
	requireFast(t, 3*time.Second, func() {
		changes := compareFractch(before.String(), after.String())
		if len(changes.Items) != 40000 {
			t.Errorf("expected 40000 added and removed costumes, got %d", len(changes.Items))
		}
	})
}

func TestCompareFractchManySameHatScriptsIsFast(t *testing.T) {
	var before, after strings.Builder
	for i := 0; i < 30000; i++ {
		fmt.Fprintf(&before, "when flag {\n  say %d;\n}\n", i)
		fmt.Fprintf(&after, "when flag {\n  think %d;\n}\n", i)
	}
	requireFast(t, 5*time.Second, func() {
		changes := compareFractch(before.String(), after.String())
		if len(changes.Scripts) != 30000 {
			t.Errorf("expected 30000 script changes, got %d", len(changes.Scripts))
		}
	})
}

func TestCompareFractchDeepNestingIsFast(t *testing.T) {
	deep := "when flag {\n  say " + strings.Repeat("(", 500000) + "x ?? 1" + strings.Repeat(")", 500000) + ";\n}\n"
	requireFast(t, 3*time.Second, func() {
		compareFractch(deep, deep+"when flag {\n}\n")
	})
}

func TestSplitFractchStatementsWithoutSemicolons(t *testing.T) {
	src := "var a = 1 id \"a\"\nvar b = json([1,\n2]) id \"b\"\ncostume \"c\" file \"assets/c.svg\"\nwhen flag {\n  say 1\n  say 2\n}\n"
	statements := splitFractchStatements(src)
	if len(statements) != 4 {
		t.Fatalf("expected four statements, got %d: %q", len(statements), statements)
	}
	before := src
	after := strings.Replace(src, "var b = json([1,\n2])", "var b = json([1,\n3])", 1)
	changes := compareFractch(before, after)
	if len(changes.State) != 1 || changes.State[0].Name != "b" {
		t.Fatalf("second variable's value change was not isolated: %+v", changes)
	}
}

func TestCompareFractchReportsCostumeOrder(t *testing.T) {
	before := "sprite \"A\";\ncostume \"one\" file \"assets/one.svg\";\ncostume \"two\" file \"assets/two.svg\";\nsound \"pop\" file \"assets/pop.wav\";\n"
	after := "sprite \"A\";\ncostume \"two\" file \"assets/two.svg\";\ncostume \"one\" file \"assets/one.svg\";\nsound \"pop\" file \"assets/pop.wav\";\n"
	changes := compareFractch(before, after)
	if len(changes.Items) != 1 || changes.Items[0].Change != "reordered" || changes.Items[0].Kind != "costume" || changes.Items[0].After != "two\none" {
		t.Fatalf("costume reorder not reported: %+v", changes.Items)
	}
	if again := compareFractch(before, before); len(again.Items) != 0 {
		t.Fatalf("unchanged order reported: %+v", again.Items)
	}
}

func TestCompareFractchSourcesReportsScriptMovedBetweenRegions(t *testing.T) {
	script := "when flag at 0,0 {\n  say 1;\n}\n"
	other := "when key \"a\" pressed {\n  say 2;\n}\n"
	before := []fractchSource{{Path: "Cat/main.fractch", Text: "sprite \"Cat\";\n" + script + other}}
	after := []fractchSource{{Path: "Cat/Enemies.fractch", Text: script}, {Path: "Cat/main.fractch", Text: "sprite \"Cat\";\n" + other}}
	changes := compareFractchSources(before, after, assetLookup{before: noAssets, after: noAssets})
	if len(changes.Scripts) != 1 || changes.Scripts[0].Change != "moved" || changes.Scripts[0].File != "Cat/Enemies.fractch" || changes.Scripts[0].BeforeFile != "Cat/main.fractch" {
		t.Fatalf("region move not reported: %+v", changes.Scripts)
	}
}

func TestSplitFractchStatementsKeepsFreeCommentsApart(t *testing.T) {
	src := "// a note on its own\n\nwhen flag {\n  say 1;\n}\n// attached\nwhen key \"a\" pressed {\n}\n"
	statements := splitFractchStatements(src)
	if len(statements) != 3 || statements[0] != "// a note on its own" || !strings.HasPrefix(statements[2], "// attached\nwhen key") {
		t.Fatalf("statements %q", statements)
	}
	edited := strings.Replace(src, "a note on its own", "a better note", 1)
	changes := compareFractch(src, edited)
	if len(changes.Scripts) != 0 || len(changes.Items) != 2 {
		t.Fatalf("editing a free comment changed a script: %+v", changes)
	}
}

func TestInspectChangesReportsRedrawnCostumeWithoutSpriteChange(t *testing.T) {
	repo := newTestRepository()
	source := repo.object("blob", []byte(fractchBase))
	first := repo.commit(repo.nestedTree(map[string]string{"Cat/main.fractch": source, "Cat/assets/Idle.svg": repo.object("blob", []byte("<svg/>"))}), "", "Initial version")
	second := repo.commit(repo.nestedTree(map[string]string{"Cat/main.fractch": source, "Cat/assets/Idle.svg": repo.object("blob", []byte("<svg id=\"new\"/>"))}), first, "Redraw")
	result := decodeResult(t, Inspect(repo.write(t), second, "changes", "", second))
	targets := result["targets"].([]any)
	if len(targets) != 1 {
		t.Fatalf("targets %v", targets)
	}
	items := targets[0].(map[string]any)["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["change"] != "edited" || items[0].(map[string]any)["name"] != "Idle" {
		t.Fatalf("redrawn costume not named: %v", items)
	}
	if result["totals"].(map[string]any)["stateOnly"] != false {
		t.Fatalf("redraw counted as state only: %v", result["totals"])
	}
}

func TestInspectChangesMarksTargetsPastTheBudget(t *testing.T) {
	repo := newTestRepository()
	large := "sprite \"A\";\nvar x = \"" + strings.Repeat("a", maxInlineDiffFileBytes+10) + "\" id \"x\";\n"
	first := repo.commit(repo.nestedTree(map[string]string{"A/main.fractch": repo.object("blob", []byte("sprite \"A\";\n"))}), "", "Initial version")
	second := repo.commit(repo.nestedTree(map[string]string{"A/main.fractch": repo.object("blob", []byte(large))}), first, "Grow")
	result := decodeResult(t, Inspect(repo.write(t), second, "changes", "", second))
	target := result["targets"].([]any)[0].(map[string]any)
	if target["tooLarge"] != true || result["totals"].(map[string]any)["stateOnly"] != false {
		t.Fatalf("oversized sprite not marked: %v %v", target, result["totals"])
	}
}

func FuzzCompareFractch(f *testing.F) {
	f.Add(fractchBase, strings.Replace(fractchBase, "turn 15", "turn 30", 1))
	f.Add("when flag { say (a ?? (b ?? \"x\")); }", "when flag { say \"\"\"raw } ;\"\"\" }")
	f.Add("var a = 1\nvar b = [\nwhen", "/* open comment\n` tick")
	f.Add("((((((((((?? ,;", "))))))) ?? }}}{{{")
	f.Fuzz(func(t *testing.T, before, after string) {
		if len(before) > 4096 || len(after) > 4096 {
			return
		}
		compareFractch(before, after)
		stripHiddenValues(before)
		camelIdentifiers(after)
	})
}
