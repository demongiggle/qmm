package server

import "testing"

func TestSplitPrefixBindings(t *testing.T) {
	// "|" and "-" replace the tmux-style "%" and "\"" split keys.
	for _, seq := range []string{"|", "-"} {
		if !isExactBinding(seq) {
			t.Errorf("prefix %q is not bound", seq)
		}
	}
	for _, legacy := range []string{"%", "\""} {
		if isExactBinding(legacy) {
			t.Errorf("legacy split key %q is still bound", legacy)
		}
	}
}
