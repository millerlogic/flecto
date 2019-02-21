package userinput

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

// TermInput reads input from the terminal.
type TermInput struct {
	m  sync.Mutex
	ch chan string
}

var _ Interface = &TermInput{}

var htmlRegexp = regexp.MustCompile(`<[^>]*>|&[^;]*;`)

// GetInput implements Input; Run must be called first. Instance thread safe when properly using Run.
// What the user inputs is not validated, it simply returns the first thing the user enters.
func (input *TermInput) GetInput(ctx context.Context, output string, choices ...Choice) (string, error) {
	input.m.Lock()
	defer input.m.Unlock()
	buf := &strings.Builder{}
	buf.WriteString("\n----------\n")
	outputText := htmlRegexp.ReplaceAllString(output, "")
	buf.WriteString(strings.Trim(outputText, "\r\n"))
	if len(choices) != 0 {
		numDefaults := 0
		allShortcuts := true
		anyUppercaseShortcuts := false
		buf.WriteString("\n\n * Please input one of: ")
		for _, opt := range choices {
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
			for _, opt := range choices {
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
