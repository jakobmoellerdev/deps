package actions

import (
	"strings"
	"testing"
)

func TestDocSetupTableListsEverySetupAction(t *testing.T) {
	var s string
	for _, sec := range Doc.Sections {
		if sec.Title == "Setup action inputs" {
			s = sec.Body
		}
	}
	if s == "" {
		t.Fatal("no Setup action inputs section")
	}
	for a, in := range setupActions {
		row := "| `" + a + "` | `" + in.input + "` | `" + in.datasource + "` | `" + in.name + "` |"
		if !strings.Contains(s, row) {
			t.Errorf("setup table lacks row %q:\n%s", row, s)
		}
	}
}
