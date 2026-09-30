package main

import (
	"os"
	"testing"
)

// The committed SVGs must be the generator's output: edit main.go, then run
// make docs-svg.
func TestSVGsUpToDate(t *testing.T) {
	for _, a := range animations {
		want, err := os.ReadFile("../../" + a.file)
		if err != nil {
			t.Fatal(err)
		}
		if got, _ := render(a); got != string(want) {
			t.Errorf("%s is out of date: run make docs-svg", a.file)
		}
	}
}
