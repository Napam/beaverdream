package webmaster

import (
	"strings"
	"testing"
)

func assertProblem(t *testing.T, problems []string, want string) {
	t.Helper()
	for _, problem := range problems {
		if strings.Contains(problem, want) {
			return
		}
	}
	t.Fatalf("problems %q have no %q", problems, want)
}

// A script with a syntax error kills its whole block, so the page stops
// working. The line number points into the html the model wrote.
func TestCheckPageReportsASyntaxError(t *testing.T) {
	page := "<p>hi</p>\n<script>\nlet a = ;\n</script>"
	problems := CheckPage(page)
	if len(problems) != 1 {
		t.Fatalf("problems = %q, want one", problems)
	}
	assertProblem(t, problems, "html line 3")
	assertProblem(t, problems, "let a = ;")
}

// Classic scripts share one global scope. A let declared in two blocks is one
// program's duplicate, and the browser refuses the second block.
func TestCheckPageCatchesADuplicateLetAcrossClassicScripts(t *testing.T) {
	page := "<script>let shared = 1;</script>\n<script>let shared = 2;</script>"
	problems := CheckPage(page)
	if len(problems) != 1 {
		t.Fatalf("problems = %q, want one", problems)
	}
	assertProblem(t, problems, "already been declared")
	assertProblem(t, problems, "html line 2")
}

// A classic script cannot use import; the browser runs it as a script, not a
// module, so the statement is a syntax error.
func TestCheckPageRejectsAnImportInAClassicScript(t *testing.T) {
	problems := CheckPage(`<script>import "three";</script>`)
	if len(problems) == 0 {
		t.Fatal("a classic import passed the check")
	}
	assertProblem(t, problems, "html line 1")
}

// A bare specifier only resolves through an import map. Without one the module
// fails before any of its code runs.
func TestCheckPageReportsABareImportMissingFromTheMap(t *testing.T) {
	page := `<script type="importmap">{"imports":{"three/":"/static/three/"}}</script>` +
		"\n" +
		`<script type="module">` +
		"\n" +
		`import { clone } from "lodash";` +
		"\n" +
		`</script>`
	problems := CheckPage(page)
	assertProblem(t, problems, "lodash")
	assertProblem(t, problems, "html line 3")
}

// A key ending in / maps every name under it, which is how three's addons are
// reachable without a key for each file.
func TestCheckPageAcceptsABareImportUnderAPrefixKey(t *testing.T) {
	page := `<script type="importmap">{"imports":{"three/":"/static/three/"}}</script>` +
		"\n" +
		`<script type="module">` +
		"\n" +
		`import { OrbitControls } from "three/OrbitControls.js";` +
		`import * as THREE from "three";` +
		"\n" +
		`</script>`
	if problems := CheckPage(page); problems != nil {
		t.Fatalf("problems = %q, want none", problems)
	}
}

// The code-owned import map always declares these names, so a module that
// imports them needs no map of its own in the page.
func TestCheckPageAcceptsTheShellImportNames(t *testing.T) {
	page := `<script type="module">` +
		`import * as THREE from "three";` +
		`import { UnrealBloomPass } from "three/addons";` +
		`import { zzfx } from "zzfx";` +
		`</script>`
	if problems := CheckPage(page); problems != nil {
		t.Fatalf("problems = %q, want none", problems)
	}
}

// A script whose type is data is not JavaScript. CheckPage must not parse it,
// or every page with a JSON state tag would look broken.
func TestCheckPageSkipsNonJavaScriptBlocks(t *testing.T) {
	page := `<script type="application/json">{"a": }</script>` +
		`<script type="application/ld+json">{ not js }</script>` +
		`<script type="text/template"><div>{{posts}}</div></script>` +
		`<script type="text/x-handlebars">let = ;</script>` +
		`<script type="speculationrules">let = ;</script>`
	if problems := CheckPage(page); problems != nil {
		t.Fatalf("problems = %q, want none", problems)
	}
}

// An import map that does not parse is its own error: the browser ignores the
// whole map, so every bare import under it fails too.
func TestCheckPageReportsABrokenImportMap(t *testing.T) {
	for _, page := range []string{
		`<script type="importmap">{"imports": </script>`,
		`<script type="importmap"></script>`,
	} {
		problems := CheckPage(page)
		if len(problems) != 1 {
			t.Fatalf("%q: problems = %q, want one", page, problems)
		}
		assertProblem(t, problems, "import map is not valid JSON")
	}
}

func TestCheckPageAcceptsACleanPage(t *testing.T) {
	page := "<script>const x = 1; console.log(x);</script>" +
		`<script type="module">import * as THREE from "three"; console.log(THREE);</script>` +
		PostsPlaceholder
	if problems := CheckPage(page); problems != nil {
		t.Fatalf("problems = %q, want none", problems)
	}
}

// The bare names CheckPage accepts have to be the ones the code-owned import
// map actually declares, or a page that imports a new shell name is refused.
func TestShellImportNamesMatchThePageImportMap(t *testing.T) {
	doc := SiteDoc(Site{HTML: PostsPlaceholder}, nil)
	_, _, raw, _ := pageScripts(doc)
	keys, err := importMapKeys(raw)
	if err != nil {
		t.Fatalf("read the page's import map: %v", err)
	}
	for key := range keys {
		if !importAllowed(key, shellImportKeys()) {
			t.Errorf("shell import map declares %q, which CheckPage does not allow", key)
		}
	}
}

// The list is short: three lines, then a count of the rest.
func TestCheckPageCapsTheProblemList(t *testing.T) {
	page := `<script type="module">let a = ;</script>` + "\n" +
		`<script type="module">let b = ;</script>` + "\n" +
		`<script type="module">let c = ;</script>` + "\n" +
		`<script type="module">let d = ;</script>`
	problems := CheckPage(page)
	if len(problems) != maxPageProblems+1 {
		t.Fatalf("problems = %q, want %d lines plus the count", problems, maxPageProblems)
	}
	if !strings.HasPrefix(problems[len(problems)-1], "and ") {
		t.Fatalf("last line = %q, want the count", problems[len(problems)-1])
	}
}
