package gitinspection

import (
	"math/rand"
	"strconv"
	"strings"
	"sync"
	"testing"
)

type benchProject struct {
	path  string
	head  string
	files []string
}

func buildBenchProject(t testing.TB, textFiles, assetFiles, commits int) benchProject {
	random := rand.New(rand.NewSource(1))
	repository := newTestRepository()
	files := map[string]string{}
	paths := []string{}
	for i := 0; i < textFiles; i++ {
		path := "sprites/sprite" + strconv.Itoa(i) + "/code.fractch"
		size := 300 + random.Intn(30000)
		files[path] = repository.object("blob", []byte(strings.Repeat("say \"hello\"\n", size/12)))
		paths = append(paths, path)
	}
	for i := 0; i < assetFiles; i++ {
		path := "assets/asset" + strconv.Itoa(i) + ".png"
		data := make([]byte, 64<<10+random.Intn(512<<10))
		random.Read(data)
		files[path] = repository.object("blob", data)
		paths = append(paths, path)
	}
	head := ""
	for i := 0; i < commits; i++ {
		edited := paths[random.Intn(textFiles)]
		files[edited] = repository.object("blob", []byte("say \"version "+strconv.Itoa(i)+"\"\n"))
		head = repository.commit(repository.nestedTree(files), head, "Save "+strconv.Itoa(i))
	}
	file := repository.write(t)
	return benchProject{path: file, head: head, files: paths}
}

func benchmarkFileBurst(b *testing.B, project benchProject) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		resetArchiveCacheForTest()
		var group sync.WaitGroup
		for _, path := range project.files {
			group.Add(1)
			go func(path string) {
				defer group.Done()
				result := Inspect(project.path, project.head, "file", path, project.head)
				if !strings.Contains(result, `"ok":true`) {
					b.Errorf("file %s failed: %.120s", path, result)
				}
			}(path)
		}
		group.Wait()
	}
}

func BenchmarkInspectFileBurst(b *testing.B) {
	benchmarkFileBurst(b, buildBenchProject(b, 180, 40, 300))
}

func BenchmarkInspectTreeRepeated(b *testing.B) {
	project := buildBenchProject(b, 180, 40, 300)
	resetArchiveCacheForTest()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		result := Inspect(project.path, project.head, "tree", "", project.head)
		if !strings.Contains(result, `"ok":true`) {
			b.Fatalf("tree failed: %.120s", result)
		}
	}
}
