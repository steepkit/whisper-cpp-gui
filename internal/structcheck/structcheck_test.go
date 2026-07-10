// Package structcheck enforces repository-wide structural rules that must
// not rely on prompt discipline alone (docs/implementation_plan.md §12.5):
// no external dependencies, no shell-mediated child processes, one-way
// layering, externalized UI strings, and unbroken docs references.
package structcheck

import (
	"encoding/json"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode"
)

const modulePath = "github.com/steepkit/whisper-cpp-gui"

// repoRoot returns the repository root, assuming this package lives at
// internal/structcheck.
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolving repo root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("repo root %s has no go.mod: %v", root, err)
	}
	return root
}

// TestNoExternalDependencies fails when go.mod declares any require,
// keeping the project on the Go standard library only.
func TestNoExternalDependencies(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repoRoot(t), "go.mod"))
	if err != nil {
		t.Fatalf("reading go.mod: %v", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "require") {
			t.Errorf("go.mod declares a dependency (%q); the project must use the standard library only", trimmed)
		}
	}
}

func goFiles(t *testing.T, root string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if name == "testdata" || strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking repo: %v", err)
	}
	return files
}

// TestNoShellCommand fails when a Go file starts a child process through a
// shell. Child processes must be exec'd with an argument slice.
func TestNoShellCommand(t *testing.T) {
	shellCall := regexp.MustCompile(`Command(?:Context)?\(\s*(?:[A-Za-z0-9_.]+,\s*)?"(?:/bin/|/usr/bin/)?(?:sh|bash|zsh|dash)"`)
	for _, path := range goFiles(t, repoRoot(t)) {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		if loc := shellCall.Find(data); loc != nil {
			t.Errorf("%s: child process started via shell (%q); pass an argument slice to the real binary instead", path, loc)
		}
	}
}

// layerRules maps a package tree (relative to repo root, applied
// recursively to subpackages) to import prefixes it must never use.
// Dependency direction: server -> job -> exec (downward-only). job and
// exec must not know HTTP exists; nothing may import server; model is a
// leaf consumed by server/main only.
var layerRules = map[string][]string{
	"internal/exec": {
		"net/http",
		modulePath + "/internal/server",
		modulePath + "/internal/job",
		modulePath + "/internal/model",
	},
	"internal/job": {
		"net/http",
		modulePath + "/internal/server",
		modulePath + "/internal/model",
	},
	"internal/model": {
		modulePath + "/internal/server",
		modulePath + "/internal/job",
		modulePath + "/internal/exec",
	},
	"internal/server": {
		// server is the top layer; nothing below may import it, and no
		// package may import main. Keeping an (empty of upward deps) entry
		// here ensures new rules are added deliberately when server grows.
	},
}

// TestLayerBoundaries parses imports of each constrained package tree
// (including subpackages) and fails on any import that violates the
// one-way layering rules.
func TestLayerBoundaries(t *testing.T) {
	root := repoRoot(t)
	fset := token.NewFileSet()
	for dir, forbidden := range layerRules {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
			if err != nil {
				t.Fatalf("parsing %s: %v", path, err)
			}
			for _, imp := range f.Imports {
				impPath := strings.Trim(imp.Path.Value, `"`)
				for _, bad := range forbidden {
					if impPath == bad || strings.HasPrefix(impPath, bad+"/") {
						t.Errorf("%s imports %q, which violates the server -> job -> exec layering", path, impPath)
					}
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walking %s: %v", dir, err)
		}
	}
}

var (
	htmlScriptStyle = regexp.MustCompile(`(?s)<(script|style)\b.*?</(script|style)>`)
	htmlComment     = regexp.MustCompile(`(?s)<!--.*?-->`)
	htmlTextNode    = regexp.MustCompile(`>\s*([^<>\s][^<>]*)<`)
	// User-visible attribute values that must come from locales instead.
	htmlTextAttr = regexp.MustCompile(`\b(title|alt|placeholder|aria-label)\s*=\s*("[^"]+"|'[^']+')`)
)

// TestNoHardcodedUIStrings fails when files under web/ (outside
// web/locales/) carry user-visible text: CJK anywhere, raw HTML text
// nodes, or literal user-facing attribute values. All user-visible strings
// must live in web/locales/*.json and be referenced via data-i18n or JS
// lookup, so any raw text node in HTML is a violation regardless of
// language.
func TestNoHardcodedUIStrings(t *testing.T) {
	root := repoRoot(t)
	webDir := filepath.Join(root, "web")
	err := filepath.WalkDir(webDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "locales" {
				return filepath.SkipDir
			}
			return nil
		}
		ext := filepath.Ext(path)
		if ext != ".html" && ext != ".js" && ext != ".css" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(data), "\n") {
			for _, r := range line {
				if unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana) {
					t.Errorf("%s:%d: hardcoded CJK text %q; move it to web/locales/*.json", path, i+1, line)
					break
				}
			}
		}
		if ext == ".html" {
			content := htmlComment.ReplaceAllString(string(data), "")
			content = htmlScriptStyle.ReplaceAllString(content, "")
			for _, m := range htmlTextNode.FindAllStringSubmatch(content, -1) {
				t.Errorf("%s: raw HTML text node %q; use data-i18n and web/locales/*.json", path, strings.TrimSpace(m[1]))
			}
			for _, m := range htmlTextAttr.FindAllStringSubmatch(content, -1) {
				t.Errorf("%s: hardcoded %s attribute %q; use a data-i18n-* mechanism backed by web/locales/*.json", path, m[1], m[2])
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking web/: %v", err)
	}
}

// TestLocaleFilesAreValidJSON keeps web/locales/*.json parseable so a broken
// locale cannot ship silently.
func TestLocaleFilesAreValidJSON(t *testing.T) {
	localeDir := filepath.Join(repoRoot(t), "web", "locales")
	entries, err := os.ReadDir(localeDir)
	if os.IsNotExist(err) {
		t.Skip("web/locales does not exist yet (pre-M2)")
	}
	if err != nil {
		t.Fatalf("reading %s: %v", localeDir, err)
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(localeDir, e.Name()))
		if err != nil {
			t.Fatalf("reading locale %s: %v", e.Name(), err)
		}
		var v map[string]any
		if err := json.Unmarshal(data, &v); err != nil {
			t.Errorf("locale %s is not valid JSON: %v", e.Name(), err)
		}
	}
}

// TestDocsLinksResolve fails when a relative markdown link in the project
// documentation points at a missing file.
func TestDocsLinksResolve(t *testing.T) {
	root := repoRoot(t)
	var mdFiles []string
	for _, dir := range []string{".", "docs", "docs/adr", "docs/reviews/bootstrap", "agent_docs", "agent_docs/codex"} {
		entries, err := os.ReadDir(filepath.Join(root, dir))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatalf("reading %s: %v", dir, err)
		}
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
				mdFiles = append(mdFiles, filepath.Join(root, dir, e.Name()))
			}
		}
	}
	link := regexp.MustCompile(`\]\(([^)\s]+)\)`)
	for _, path := range mdFiles {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		for _, m := range link.FindAllStringSubmatch(string(data), -1) {
			target := m[1]
			if strings.Contains(target, "://") || strings.HasPrefix(target, "mailto:") || strings.HasPrefix(target, "#") {
				continue
			}
			target = strings.SplitN(target, "#", 2)[0]
			resolved := filepath.Join(filepath.Dir(path), target)
			if _, err := os.Stat(resolved); err != nil {
				t.Errorf("%s: broken docs link %q (resolved to %s)", path, m[1], resolved)
			}
		}
	}
}
