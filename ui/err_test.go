package ui

import (
	"errors"
	"strings"
	"testing"
)

func TestErrBox_Fits(t *testing.T) {
	cases := []struct {
		name  string
		width int
		err   error
		want  bool
	}{
		{"nil error always fits", 80, nil, true},
		{"short error fits wide box", 80, errors.New("oops"), true},
		{"no width set: anything fits", 0, errors.New("very long error message here"), true},
		{"exact boundary fits", 10, errors.New("1234567"), true},          // 7 chars, box 10 => 10-3=7 limit
		{"one over limit doesn't fit", 10, errors.New("12345678"), false}, // 8 chars > 7
		{"multiline never fits", 80, errors.New("line1\nline2"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := NewErrBox()
			e.SetSize(tc.width, 1)
			if got := e.Fits(tc.err); got != tc.want {
				t.Errorf("Fits() = %v, want %v (width=%d, err=%v)", got, tc.want, tc.width, tc.err)
			}
		})
	}
}

func TestErrBox_HasError(t *testing.T) {
	e := NewErrBox()
	if e.HasError() {
		t.Fatal("fresh ErrBox should have no error")
	}
	e.SetError(errors.New("something bad"))
	if !e.HasError() {
		t.Fatal("HasError should be true after SetError")
	}
	e.Clear()
	if e.HasError() {
		t.Fatal("HasError should be false after Clear")
	}
}

func TestErrBox_String_EmptyWhenNoError(t *testing.T) {
	e := NewErrBox()
	e.SetSize(80, 1)
	if got := e.String(); got != "" {
		t.Errorf("String() with no error = %q, want empty", got)
	}
}

func TestErrBox_String_ContainsErrorText(t *testing.T) {
	e := NewErrBox()
	e.SetSize(80, 1)
	e.SetError(errors.New("something went wrong"))
	if got := e.String(); !strings.Contains(got, "something went wrong") {
		t.Errorf("String() = %q, expected to contain error text", got)
	}
}

func TestErrBox_String_FlattenMultiline(t *testing.T) {
	e := NewErrBox()
	e.SetSize(80, 1)
	e.SetError(errors.New("line one\nline two"))
	got := e.String()
	if strings.Contains(got, "\n") {
		t.Errorf("String() should flatten newlines, got %q", got)
	}
	if !strings.Contains(got, "//") {
		t.Errorf("String() should join multiline with //, got %q", got)
	}
}

func TestErrBox_SetNotice_Info(t *testing.T) {
	e := NewErrBox()
	e.SetSize(80, 1)
	e.SetNotice("branch copied", NoticeInfo)

	if !e.HasContent() {
		t.Fatal("HasContent should be true after SetNotice")
	}
	if e.HasError() {
		t.Fatal("an info notice must not report HasError")
	}
	if got := e.String(); !strings.Contains(got, "branch copied") {
		t.Errorf("String() = %q, expected to contain notice text", got)
	}
}

func TestErrBox_SetNotice_ErrorLevelReportsHasError(t *testing.T) {
	e := NewErrBox()
	e.SetNotice("boom", NoticeError)
	if !e.HasError() {
		t.Fatal("an error-level notice must report HasError")
	}
}

func TestErrBox_InfoAndErrorStyleDiffer(t *testing.T) {
	// No profile to force: v2 styles always emit full fidelity, so the two styles
	// differ in the rendered bytes without any global being mutated. v1 needed the
	// package-global renderer pinned here or both rendered as plain text.

	info := NewErrBox()
	info.SetSize(80, 1)
	info.SetNotice("same text", NoticeInfo)

	err := NewErrBox()
	err.SetSize(80, 1)
	err.SetError(errors.New("same text"))

	if info.String() == err.String() {
		t.Error("info (neutral) and error (danger) must render with different styling")
	}
}

func TestErrBox_ClearResetsContent(t *testing.T) {
	e := NewErrBox()
	e.SetNotice("branch copied", NoticeInfo)
	e.Clear()
	if e.HasContent() {
		t.Fatal("HasContent should be false after Clear")
	}
	if got := e.String(); got != "" {
		t.Errorf("String() after Clear = %q, want empty", got)
	}
}
