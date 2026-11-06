// Package docscheck keeps the documentation honest: every link and image
// resolves, every page is reachable from the README, and every page named in
// backticks exists.
package docscheck

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

const root = "../.."

var (
	linkRe   = regexp.MustCompile(`\]\(([^)\s]+)\)`)
	pageRe   = regexp.MustCompile("`([a-z][a-z0-9-]*\\.md)`")
	headRe   = regexp.MustCompile(`(?m)^#{1,6}\s+(.+?)\s*$`)
	slugDrop = regexp.MustCompile(`[^a-z0-9 _-]`)
)

func markdownFiles(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, pat := range []string{"*.md", "docs/*.md", "deploy/*.md", "examples/*.md"} {
		m, _ := filepath.Glob(filepath.Join(root, pat))
		out = append(out, m...)
	}
	sort.Strings(out)
	return out
}

// slug is GitHub's heading anchor: lower case, punctuation dropped, spaces to dashes.
func slug(h string) string {
	h = strings.ToLower(strings.NewReplacer("`", "", "*", "").Replace(h))
	return strings.ReplaceAll(slugDrop.ReplaceAllString(h, ""), " ", "-")
}

func anchors(t *testing.T, path string) map[string]bool {
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]bool{}
	for _, h := range headRe.FindAllStringSubmatch(string(b), -1) {
		m[slug(h[1])] = true
	}
	return m
}

func TestLinksAndImagesResolve(t *testing.T) {
	for _, f := range markdownFiles(t) {
		b, _ := os.ReadFile(f)
		text := string(b)
		// Code blocks hold examples, not links.
		text = regexp.MustCompile("(?s)```.*?```").ReplaceAllString(text, "")
		for _, m := range linkRe.FindAllStringSubmatch(text, -1) {
			target := m[1]
			if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") || strings.HasPrefix(target, "mailto:") {
				continue
			}
			file, frag, _ := strings.Cut(target, "#")
			dest := f
			if file != "" {
				dest = filepath.Join(filepath.Dir(f), file)
				if _, err := os.Stat(dest); err != nil {
					t.Errorf("%s: link %q: %s does not exist", rel(f), target, rel(dest))
					continue
				}
			}
			if frag != "" && strings.HasSuffix(dest, ".md") && !anchors(t, dest)[frag] {
				t.Errorf("%s: link %q: no heading #%s in %s", rel(f), target, frag, rel(dest))
			}
		}
	}
}

func rel(p string) string { r, _ := filepath.Rel(root, p); return r }

func TestEveryPageIsInTheREADMEIndex(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	readme := string(b)
	pages, _ := filepath.Glob(filepath.Join(root, "docs", "*.md"))
	for _, p := range pages {
		if !strings.Contains(readme, "docs/"+filepath.Base(p)) {
			t.Errorf("README.md does not list %s", rel(p))
		}
	}
}

func TestPagesNamedInBackticksExist(t *testing.T) {
	exists := func(name string) bool {
		for _, d := range []string{"docs", "."} {
			if _, err := os.Stat(filepath.Join(root, d, name)); err == nil {
				return true
			}
		}
		return false
	}
	for _, f := range markdownFiles(t) {
		if strings.HasSuffix(f, "CHANGELOG.md") {
			continue
		}
		b, _ := os.ReadFile(f)
		for _, m := range pageRe.FindAllStringSubmatch(string(b), -1) {
			if !exists(m[1]) {
				t.Errorf("%s names `%s`, which does not exist", rel(f), m[1])
			}
		}
	}
}
