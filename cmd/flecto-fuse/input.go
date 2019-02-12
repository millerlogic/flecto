package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"unicode"
)

// InputOption is a user input option for use with Input.GetInput
type InputOption struct {
	Text     string // the text the user can input.
	Shortcut rune   // leave zero for no shortcut.
	Default  bool   // set to true if it's the default option.
}

// Input gets user input.
// GetInput's output string is what is presented to the user. The returned string is the user input.
// The output string can contain basic HTML tags and entities, which may be used or stripped.
// Waits until either the user inputs or the ctx is done.
type Input interface {
	GetInput(ctx context.Context, output string, options ...InputOption) (string, error)
}

// TermInput reads input from the terminal.
type TermInput struct {
	m  sync.Mutex
	ch chan string
}

var _ Input = &TermInput{}

var htmlRegexp = regexp.MustCompile(`<[^>]*>|&[^;]*;`)

// GetInput implements Input; Run must be called first. Instance thread safe when properly using Run.
// What the user inputs is not validated, it simply returns the first thing the user enters.
func (input *TermInput) GetInput(ctx context.Context, output string, options ...InputOption) (string, error) {
	input.m.Lock()
	defer input.m.Unlock()
	buf := &strings.Builder{}
	buf.WriteString("\n----------\n")
	outputText := htmlRegexp.ReplaceAllString(output, "")
	buf.WriteString(strings.Trim(outputText, "\r\n"))
	if len(options) != 0 {
		numDefaults := 0
		allShortcuts := true
		anyUppercaseShortcuts := false
		buf.WriteString("\n\n * Please input one of: ")
		for _, opt := range options {
			if opt.Default {
				numDefaults++
			}
			buf.WriteString(opt.Text)
			if opt.Shortcut == 0 {
				allShortcuts = false
				buf.WriteString("  ")
			} else {
				if unicode.IsUpper(opt.Shortcut) {
					anyUppercaseShortcuts = true
				}
				buf.WriteString(" (")
				buf.WriteRune(opt.Shortcut)
				buf.WriteString(")  ")
			}
		}
		if allShortcuts {
			buf.WriteString("\t[")
			for _, opt := range options {
				if opt.Default && numDefaults == 1 && !anyUppercaseShortcuts {
					buf.WriteRune(unicode.ToUpper(opt.Shortcut))
				} else {
					buf.WriteRune(opt.Shortcut)
				}
			}
			buf.WriteString("] ")
		}
	}
	buf.WriteString("\n----------\n")
	os.Stdout.WriteString(buf.String())
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case text := <-input.ch:
		return text, nil
	}
}

// Run reads terminal input.
// Doesn't return until the ctx is done, however it has to wait until a line is read from stdin or EOF.
// Not thread safe: must be called and return before any GetInput calls.
func (input *TermInput) Run(ctx context.Context) {
	ch := make(chan string)
	input.ch = ch
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		select {
		case ch <- scanner.Text():
		case <-ctx.Done():
			return
		default:
			fmt.Printf("(unexpected input)\n")
		}
	}
}
