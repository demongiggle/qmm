package main

import (
	"reflect"
	"strings"
	"testing"

	"github.com/demongiggle/qmm/internal/client"
	"github.com/demongiggle/qmm/internal/protocol"
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

func TestFormatSessionLine(t *testing.T) {
	got := formatSessionLine(protocol.SessionInfo{
		Name:    "default",
		Windows: 2,
		Panes:   3,
		Clients: 1,
		Version: "v0.2.0",
	})
	want := "default: 2 windows (3 panes, 1 attached) [v0.2.0]"
	if got != want {
		t.Fatalf("formatSessionLine = %q, want %q", got, want)
	}

	gotSingle := formatSessionLine(protocol.SessionInfo{
		Name:    "work",
		Windows: 1,
		Panes:   1,
	})
	wantSingle := "work: 1 window (1 pane)"
	if gotSingle != wantSingle {
		t.Fatalf("formatSessionLine = %q, want %q", gotSingle, wantSingle)
	}
}

func TestExecArgsForSwitch(t *testing.T) {
	tests := []struct {
		req  *client.SwitchRequest
		want []string
	}{
		{&client.SwitchRequest{Name: "work", Create: false}, []string{"qmm", "attach", "work"}},
		{&client.SwitchRequest{Name: "1", Create: true}, []string{"qmm", "new", "1"}},
		{&client.SwitchRequest{Name: "", Create: true}, []string{"qmm", "new"}},
		{&client.SwitchRequest{Name: "", Create: false}, []string{"qmm", "attach"}},
	}
	for _, tc := range tests {
		got := execArgsForSwitch(tc.req)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("execArgsForSwitch(%+v) = %v, want %v", tc.req, got, tc.want)
		}
	}
}

func TestRunListEmptyDir(t *testing.T) {
	dir := t.TempDir()
	var out strings.Builder
	if err := runList(nil, dir, &out); err != nil {
		t.Fatalf("runList: %v", err)
	}
	if !strings.Contains(out.String(), "no qmm sessions running") {
		t.Fatalf("output = %q, want no sessions message", out.String())
	}
}

func TestRunKillSessionValidatesArgs(t *testing.T) {
	var out strings.Builder
	if err := runKillSession(nil, t.TempDir(), &out); err == nil {
		t.Fatal("expected error for missing session name")
	}
	if err := runKillSession([]string{"bad/name"}, t.TempDir(), &out); err == nil {
		t.Fatal("expected error for invalid session name")
	}
}
