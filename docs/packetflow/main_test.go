package main

import (
	"os"
	"testing"
)

// The committed SVG must be the generator's output: edit main.go, then run
// make docs-svg.
func TestSVGUpToDate(t *testing.T) {
	want, err := os.ReadFile("../packet-flow.svg")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := render(); got != string(want) {
		t.Fatal("docs/packet-flow.svg is out of date: run make docs-svg")
	}
}
