// Package doclinks checks the links of the developer docs (docs/*.md,
// README.md, lab/README.md), which GitHub renders without checking anything:
// relative links must point to existing files and headings, links to the
// website (https://mwindower.github.io/open-dci/docs/...) to an existing page
// and heading in website/docs, and the website's links into docs/ likewise.
// The website's own links are checked by its build (onBrokenLinks: throw).
package doclinks

import (
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const (
	root = "../.."
	site = "https://mwindower.github.io/open-dci/docs/"
	repo = "https://github.com/mwindower/open-dci/blob/main/"
)

var (
	link    = regexp.MustCompile(`\]\(([^)\s]+)\)`)
	heading = regexp.MustCompile(`^#{1,6} (.+)$`)
	nonWord = regexp.MustCompile(`[^\p{L}\p{N}_\- ]`)
)

// slug is GitHub's (and Docusaurus') heading anchor.
func slug(h string) string {
	return strings.ReplaceAll(nonWord.ReplaceAllString(strings.ToLower(strings.TrimSpace(h)), ""), " ", "-")
}

// doc is a Markdown file: its links and its anchors, outside code blocks.
type doc struct {
	links   []string
	anchors map[string]bool
}

func parse(t *testing.T, file string) doc {
	t.Helper()
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	d := doc{anchors: map[string]bool{}}
	fence := ""
	for _, l := range strings.Split(string(raw), "\n") {
		s := strings.TrimSpace(l)
		if fence != "" || strings.HasPrefix(s, "```") {
			switch {
			case fence == "":
				fence = s[:len(s)-len(strings.TrimLeft(s, "`"))]
			case strings.HasPrefix(s, fence):
				fence = ""
			}
			continue
		}
		if m := heading.FindStringSubmatch(l); m != nil {
			d.anchors[slug(m[1])] = true
		}
		for _, m := range link.FindAllStringSubmatch(l, -1) {
			d.links = append(d.links, m[1])
		}
	}
	return d
}

type checker struct {
	t     *testing.T
	cache map[string]doc
}

func (c *checker) doc(file string) (doc, bool) {
	if d, ok := c.cache[file]; ok {
		return d, true
	}
	if _, err := os.Stat(file); err != nil {
		return doc{}, false
	}
	d := parse(c.t, file)
	c.cache[file] = d
	return d, true
}

// target checks that file exists and, for Markdown, has the anchor.
func (c *checker) target(from, file, anchor, href string) {
	fi, err := os.Stat(file)
	if err != nil {
		c.t.Errorf("%s: broken link %s", from, href)
		return
	}
	if anchor == "" || fi.IsDir() {
		return
	}
	if d, _ := c.doc(file); !d.anchors[anchor] {
		c.t.Errorf("%s: no heading #%s for %s", from, anchor, href)
	}
}

func (c *checker) check(file string) {
	d, _ := c.doc(file)
	for _, href := range d.links {
		target, anchor, _ := strings.Cut(href, "#")
		switch {
		case strings.HasPrefix(href, site):
			page := strings.TrimPrefix(target, site)
			c.target(file, filepath.Join(root, "website/docs", page+".md"), anchor, href)
		case strings.HasPrefix(href, repo+"docs/"):
			c.target(file, filepath.Join(root, strings.TrimPrefix(target, repo)), anchor, href)
		case strings.Contains(href, "://") || strings.HasPrefix(href, "mailto:"):
		case target == "":
			if !d.anchors[anchor] {
				c.t.Errorf("%s: no heading #%s", file, anchor)
			}
		case strings.HasSuffix(file, ".md") && strings.Contains(file, "website/docs"):
			// relative links within the website: checked by its build
		default:
			c.target(file, path.Join(path.Dir(file), target), anchor, href)
		}
	}
}

func TestLinks(t *testing.T) {
	c := &checker{t: t, cache: map[string]doc{}}
	files, _ := filepath.Glob(filepath.Join(root, "docs", "*.md"))
	files = append(files, filepath.Join(root, "README.md"), filepath.Join(root, "lab", "README.md"))
	site, _ := filepath.Glob(filepath.Join(root, "website", "docs", "*", "*.md"))
	if len(files) < 10 || len(site) < 10 {
		t.Fatalf("found %d repo docs and %d website pages", len(files), len(site))
	}
	for _, f := range append(files, site...) {
		c.check(f)
	}
}

func TestSlug(t *testing.T) {
	for h, want := range map[string]string{
		"What `apply` / `run` change":                                      "what-apply--run-change",
		"FRR (via `vtysh`, never touching `frr.conf`)":                     "frr-via-vtysh-never-touching-frrconf",
		"Route-target behaviour (FRR 10.6, L3VNI, tested live in the lab)": "route-target-behaviour-frr-106-l3vni-tested-live-in-the-lab",
	} {
		if got := slug(h); got != want {
			t.Errorf("slug(%q) = %q, want %q", h, got, want)
		}
	}
}
