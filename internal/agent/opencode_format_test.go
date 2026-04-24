package agent

import (
	"strings"
	"testing"
)

// TestSingleLine_CollapsesNewlines is the regression for CodeRabbit
// PR #3 inline 3134413640: TUI error formatting truncated raw HTTP
// bodies / JSON without first collapsing newlines, so a multi-line
// upstream payload would break the inbox's one-line-per-event
// contract.
func TestSingleLine_CollapsesNewlines(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, want string
	}{
		{"a\nb", "a b"},
		{"a\r\nb", "a b"},
		{"a\rb", "a b"},
		{"  hello\n\nworld  ", "hello  world"},
		{"no newlines", "no newlines"},
		{"", ""},
	}
	for _, tc := range cases {
		got := singleLine(tc.in)
		if got != tc.want {
			t.Errorf("singleLine(%q) = %q, want %q", tc.in, got, tc.want)
		}
		if strings.ContainsAny(got, "\r\n") {
			t.Errorf("singleLine(%q) = %q still contains a line break", tc.in, got)
		}
	}
}

func TestTruncate_AfterSingleLine_StaysSingleLine(t *testing.T) {
	t.Parallel()
	// Exactly the production order: collapse first, then bound. A
	// short multi-line payload survives the bound but must still be
	// flat.
	in := "first line\nsecond line"
	got := truncate(singleLine(in), 256)
	if strings.ContainsAny(got, "\r\n") {
		t.Fatalf("truncate(singleLine(%q), 256) = %q; must not contain newlines", in, got)
	}
	if got != "first line second line" {
		t.Fatalf("got %q, want %q", got, "first line second line")
	}
}
