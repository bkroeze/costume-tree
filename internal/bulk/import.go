// Package bulk parses the compact actor/item import format used by the bulk
// import screen. It deliberately has no persistence or HTTP dependencies.
package bulk

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Item is one item-type line in an import block. Line is the one-based source
// line on which the item type appeared.
type Item struct {
	Type string
	Line int
}

// Entry is a flattened actor/item pair. It is useful to callers that do not
// need to retain block boundaries while still preserving source diagnostics.
type Entry struct {
	Actor     string
	ItemType  string
	ActorLine int
	ItemLine  int
	Block     int
}

// Block starts with an actor line and contains one or more item-type lines.
// StartLine and EndLine delimit the non-empty source portion of the block.
type Block struct {
	Actor     string
	ActorLine int
	Items     []Item
	StartLine int
	EndLine   int
}

// ParseError identifies a malformed import and its source line. Line is zero
// when the input contains no non-empty source line at all.
type ParseError struct {
	Line    int
	Message string
}

func (e *ParseError) Error() string {
	if e == nil {
		return ""
	}
	if e.Line > 0 {
		return fmt.Sprintf("bulk import line %d: %s", e.Line, e.Message)
	}
	return "bulk import: " + e.Message
}

// Unwrap classifies parse failures for callers that need to distinguish an
// empty paste from a malformed block while retaining the line diagnostic.
func (e *ParseError) Unwrap() error {
	if e != nil && e.Line == 0 {
		return ErrEmptyInput
	}
	return ErrMalformed
}

var (
	ErrEmptyInput = errors.New("bulk import: input is empty")
	ErrMalformed  = errors.New("bulk import: malformed block")
)

// Parse parses blocks separated by one or more blank lines. Line endings are
// normalized from CRLF and CR to LF before parsing. Leading/trailing spaces on
// values are removed; original one-based line numbers are retained.
//
// A block must contain an actor and at least one item type. Blank lines at the
// beginning and end are ignored. The parser rejects empty input and actor-only
// blocks rather than manufacturing incomplete records.
func Parse(input string) ([]Block, error) {
	input = strings.ReplaceAll(input, "\r\n", "\n")
	input = strings.ReplaceAll(input, "\r", "\n")
	lines := strings.Split(input, "\n")

	blocks := make([]Block, 0)
	var current *Block
	flush := func() error {
		if current == nil {
			return nil
		}
		if len(current.Items) == 0 {
			return &ParseError{Line: current.ActorLine, Message: "block must contain an actor and at least one item type"}
		}
		blocks = append(blocks, *current)
		current = nil
		return nil
	}

	for lineNumber, raw := range lines {
		line := lineNumber + 1
		value := strings.TrimSpace(raw)
		if value == "" {
			if err := flush(); err != nil {
				return nil, err
			}
			continue
		}
		if !utf8.ValidString(value) {
			return nil, &ParseError{Line: line, Message: "line is not valid UTF-8"}
		}
		if current == nil {
			current = &Block{Actor: value, ActorLine: line, StartLine: line, EndLine: line}
			continue
		}
		current.Items = append(current.Items, Item{Type: value, Line: line})
		current.EndLine = line
	}
	if err := flush(); err != nil {
		return nil, err
	}
	if len(blocks) == 0 {
		return nil, &ParseError{Line: 0, Message: ErrEmptyInput.Error()}
	}
	return blocks, nil
}

// ParseEntries is a convenience form of Parse that retains line diagnostics
// while flattening each actor/item pair in source order.
func ParseEntries(input string) ([]Entry, error) {
	blocks, err := Parse(input)
	if err != nil {
		return nil, err
	}
	entries := make([]Entry, 0)
	for blockIndex, block := range blocks {
		for _, item := range block.Items {
			entries = append(entries, Entry{Actor: block.Actor, ItemType: item.Type, ActorLine: block.ActorLine, ItemLine: item.Line, Block: blockIndex + 1})
		}
	}
	return entries, nil
}

// ParseImport is retained as a descriptive alias for Parse.
func ParseImport(input string) ([]Block, error) { return Parse(input) }
