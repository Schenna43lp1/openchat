// Structural checks for templates and stylesheets.
package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestTemplateHeads(t *testing.T) {
	files, err := filepath.Glob("templates/*.html")
	if err != nil || len(files) == 0 {
		t.Fatalf("templates = %v, %v", files, err)
	}
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		before, _, found := strings.Cut(string(raw), "<head>")
		if !found || strings.Contains(before, "<meta") || strings.Contains(before, "<title>") {
			t.Errorf("%s: metadata before <head>", file)
		}
		if strings.Count(string(raw), "<title>") != 1 {
			t.Errorf("%s: needs exactly one title", file)
		}
	}
}

func TestStylesheetImports(t *testing.T) {
	raw, err := os.ReadFile("static/style.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, match := range regexp.MustCompile(`url\(\./([^)]+)\)`).FindAllStringSubmatch(string(raw), -1) {
		if _, err := os.Stat(filepath.Join("static", match[1])); err != nil {
			t.Errorf("import of %s: %v", match[1], err)
		}
	}
	tokens, err := os.ReadFile("static/css/root.css")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(tokens), "@import") {
		t.Error("@import is invalid inside :root")
	}
}
