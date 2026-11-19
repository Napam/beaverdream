package webmaster

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/evanw/esbuild/pkg/api"
	"golang.org/x/net/html"
)

// shellImports are the bare names the code-owned import map always declares,
// with the same keys SiteDoc writes into the page's head. A model module that
// imports one of them loads without any map of its own.
var shellImports = []string{"three", "three/addons", "zzfx"}

// pageScript is one <script> block found in a page, with the html line its
// content starts on.
type pageScript struct {
	content   string
	line      int
	module    bool
	importMap bool
}

// CheckPage parses the page's JavaScript and checks its bare module imports
// against the page's import map. It returns short, model-readable lines: an
// empty slice means the page's scripts will parse and load. The page is the
// model's html, so this never fails on markup, only on what a browser would
// refuse to run.
func CheckPage(page string) []string {
	classic, modules, importMap, mapLine := pageScripts(page)

	var problems []string
	problems = append(problems, checkClassic(classic)...)
	problems = append(problems, checkModules(modules)...)

	imports := shellImportKeys()
	known := true
	if mapLine > 0 {
		keys, err := importMapKeys(importMap)
		if err != nil {
			problems = append(problems, fmt.Sprintf(
				"html line %d: the import map is not valid JSON: %s",
				mapLine, strings.Join(strings.Fields(err.Error()), " "),
			))
			known = false
		}
		for key := range keys {
			imports[key] = true
		}
	}
	if known {
		problems = append(problems, missingImports(modules, imports)...)
	}

	if len(problems) > maxPageProblems {
		more := len(problems) - maxPageProblems
		problems = append(problems[:maxPageProblems], fmt.Sprintf("and %d more", more))
	}
	if len(problems) == 0 {
		return nil
	}
	return problems
}

// maxPageProblems is how many lines a page problem list keeps before it counts
// the rest, so a badly broken script does not flood the room.
const maxPageProblems = 3

// pageScripts splits the page into classic scripts, module scripts and the
// first import map, each with the html line its content starts on. mapLine is
// 0 when the page has no import map. The tokenizer, not a regex, decides where
// a script begins and ends, so a `<script>` inside a string is just text.
func pageScripts(page string) (
	classic, modules []pageScript,
	importMap string,
	mapLine int,
) {
	tokens := html.NewTokenizer(strings.NewReader(page))
	line := 1
	inScript := false
	kind := ""
	block := pageScript{}
	for {
		token := tokens.Next()
		if token == html.ErrorToken {
			return classic, modules, importMap, mapLine
		}
		raw := string(tokens.Raw())
		switch token {
		case html.StartTagToken:
			name, _ := tokens.TagName()
			if string(name) != "script" {
				break
			}
			kind = scriptKind(tokens)
			block = pageScript{
				line:      line + strings.Count(raw, "\n"),
				module:    kind == "module",
				importMap: kind == "importmap",
			}
			inScript = true
		case html.TextToken:
			// Raw, not Text: script text is not markup and its entities must
			// reach the parser untouched.
			if inScript {
				block.content += raw
			}
		case html.EndTagToken:
			if !inScript {
				break
			}
			inScript = false
			switch {
			case block.importMap:
				if mapLine == 0 {
					importMap, mapLine = block.content, block.line
				}
			case block.module:
				modules = append(modules, block)
			case kind != "":
				classic = append(classic, block)
			}
		}
		line += strings.Count(raw, "\n")
	}
}

// scriptKind reads a script's type attribute and maps it to "module",
// "importmap", "classic" for JavaScript, or "" for anything a browser would
// not run as JavaScript. An absent type is a classic script.
func scriptKind(tokens *html.Tokenizer) string {
	typ := ""
	for {
		key, value, more := tokens.TagAttr()
		if string(key) == "type" {
			typ = strings.ToLower(strings.TrimSpace(string(value)))
		}
		if !more {
			break
		}
	}
	switch typ {
	case "", "text/javascript", "application/javascript":
		return "classic"
	case "module":
		return "module"
	case "importmap":
		return "importmap"
	}
	// application/json, application/ld+json, speculationrules, text/template,
	// every text/x-* and anything else is data, not JavaScript.
	return ""
}

// checkClassic parses the classic scripts as one program, the way the browser
// runs them: they share global scope, so a let or class declared twice across
// two blocks is a syntax error. Wrapping it in a function also makes a
// top-level import or export an error, since only a module may use those.
func checkClassic(blocks []pageScript) []string {
	if len(blocks) == 0 {
		return nil
	}
	type span struct{ merged, htmlLine int }
	var spans []span
	var merged strings.Builder
	line := 1
	for _, block := range blocks {
		spans = append(spans, span{merged: line, htmlLine: block.line})
		merged.WriteString(block.content)
		// End each block as its own statement list. Without the semicolon a
		// block's last expression and the next block's first line could join
		// into one statement that neither block contains.
		merged.WriteString(";\n")
		line += strings.Count(block.content, "\n") + 1
	}
	source := merged.String()

	result := api.Transform(
		"(function(){"+source+"\n})()",
		api.TransformOptions{Loader: api.LoaderJS, Format: api.FormatIIFE},
	)
	lines := strings.Split(source, "\n")
	var problems []string
	for _, message := range result.Errors {
		if message.Location == nil {
			problems = append(problems, "html: "+oneLine(message.Text))
			continue
		}
		htmlLine := spans[0].htmlLine
		for _, candidate := range spans {
			if candidate.merged <= message.Location.Line {
				htmlLine = candidate.htmlLine + message.Location.Line - candidate.merged
			}
		}
		problems = append(problems, scriptProblem(
			htmlLine, message.Text, lineAt(lines, message.Location.Line),
		))
	}
	return problems
}

// checkModules parses each module script on its own. A module has its own
// scope, so two modules may declare the same name.
func checkModules(blocks []pageScript) []string {
	var problems []string
	for _, block := range blocks {
		result := api.Transform(
			block.content,
			api.TransformOptions{Loader: api.LoaderJS, Format: api.FormatESModule},
		)
		lines := strings.Split(block.content, "\n")
		for _, message := range result.Errors {
			if message.Location == nil {
				problems = append(problems, "html: "+oneLine(message.Text))
				continue
			}
			htmlLine := block.line + message.Location.Line - 1
			problems = append(problems, scriptProblem(
				htmlLine, message.Text, lineAt(lines, message.Location.Line),
			))
		}
	}
	return problems
}

// scriptProblem is one error as the model reads it: where in the html it sits,
// what the parser said, and the source line itself.
func scriptProblem(htmlLine int, text, source string) string {
	problem := fmt.Sprintf("html line %d: %s", htmlLine, oneLine(text))
	if source = strings.TrimSpace(source); source != "" {
		problem += ": " + clip(source, 100)
	}
	return problem
}

// oneLine flattens a parser message, which may carry line breaks of its own.
func oneLine(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

// lineAt returns one 1-based line of source, or "" past the end.
func lineAt(lines []string, line int) string {
	if line < 1 || line > len(lines) {
		return ""
	}
	return lines[line-1]
}

// importMapKeys reads the keys of an import map's "imports" object. A map with
// no imports is empty, not an error.
func importMapKeys(source string) (map[string]bool, error) {
	var parsed struct {
		Imports map[string]json.RawMessage `json:"imports"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(source)), &parsed); err != nil {
		return nil, err
	}
	keys := make(map[string]bool, len(parsed.Imports))
	for key := range parsed.Imports {
		keys[key] = true
	}
	return keys, nil
}

// shellImportKeys is the set a page starts with: the names the code-owned
// import map always provides.
func shellImportKeys() map[string]bool {
	keys := make(map[string]bool, len(shellImports))
	for _, name := range shellImports {
		keys[name] = true
	}
	return keys
}

var (
	// importFrom matches `import x from "a"`, `import {x} from "a"`,
	// `export * from "a"` and their multi-line forms.
	importFrom = regexp.MustCompile(`\b(?:import|export)\b[^'";]*?\bfrom\s*['"]([^'"\n]+)['"]`)
	// importBare matches a side-effect import at the start of a line.
	importBare = regexp.MustCompile(`(?m)^\s*import\s*['"]([^'"\n]+)['"]`)
	// importDynamic matches `import("a")`.
	importDynamic = regexp.MustCompile(`\bimport\s*\(\s*['"]([^'"\n]+)['"]\s*\)`)
	// urlScheme tells a full URL from a bare specifier.
	urlScheme = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*:`)
)

// missingImports names every bare specifier a module imports that the import
// map does not resolve, each once. Relative and absolute URLs never need a map.
func missingImports(blocks []pageScript, keys map[string]bool) []string {
	seen := map[string]bool{}
	var problems []string
	for _, block := range blocks {
		for _, finder := range []*regexp.Regexp{importFrom, importBare, importDynamic} {
			for _, match := range finder.FindAllStringSubmatchIndex(block.content, -1) {
				spec := block.content[match[2]:match[3]]
				if seen[spec] || !bareSpecifier(spec) || importAllowed(spec, keys) {
					continue
				}
				seen[spec] = true
				line := block.line + strings.Count(block.content[:match[2]], "\n")
				problems = append(problems, fmt.Sprintf(
					"html line %d: %q is not in the import map, so the module cannot load it; "+
						"import a URL, a relative path, or add it to an import map in the page",
					line, spec,
				))
			}
		}
	}
	return problems
}

// bareSpecifier reports a name that only an import map can resolve.
func bareSpecifier(spec string) bool {
	if spec == "" || strings.HasPrefix(spec, ".") || strings.HasPrefix(spec, "/") {
		return false
	}
	return !urlScheme.MatchString(spec)
}

// importAllowed reports whether the map resolves the specifier, by an exact
// key or by the longest trailing-slash prefix.
func importAllowed(spec string, keys map[string]bool) bool {
	if keys[spec] {
		return true
	}
	longest := ""
	for key := range keys {
		if strings.HasSuffix(key, "/") && strings.HasPrefix(spec, key) && len(key) > len(longest) {
			longest = key
		}
	}
	return longest != ""
}
