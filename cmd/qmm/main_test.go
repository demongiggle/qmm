package main

import (
	"strings"
	"testing"
)

func TestRunUpdateHelp(t *testing.T) {
	var stdout, stderr strings.Builder
	if err := runUpdate([]string{"--help"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("unexpected stdout for --help: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "Usage: qmm update") || !strings.Contains(stderr.String(), "--pre") {
		t.Fatalf("help output = %q", stderr.String())
	}
}

func TestRunUpdateRejectsPositionalArguments(t *testing.T) {
	var stdout, stderr strings.Builder
	err := runUpdate([]string{"latest"}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "positional arguments") {
		t.Fatalf("error = %v, want positional-argument error", err)
	}
}
