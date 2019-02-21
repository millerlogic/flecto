package userinput

import (
	"context"
)

// Choice is a user input choice for use with Input.GetInput
type Choice struct {
	Text     string // the text the user can input.
	Shortcut rune   // leave zero for no shortcut.
	Default  bool   // set to true if it's the default option.
}

// Interface gets user input.
// GetInput's output string is what is presented to the user. The returned string is the user input.
// The output string can contain basic HTML tags and entities, which may be used or stripped.
// Waits until either the user inputs or the ctx is done.
type Interface interface {
	GetInput(ctx context.Context, output string, choice ...Choice) (string, error)
}
