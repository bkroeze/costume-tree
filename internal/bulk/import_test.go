package bulk

import (
	"errors"
	"strings"
	"testing"
)

func TestParseNormalizesLineEndingsAndRetainsSourceLines(t *testing.T) {
	blocks, err := Parse("\r\n Ada \r\n Cloak\rHat\r\n\r\nBanquo\r\nSword\r\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 2 {
		t.Fatalf("got %d blocks, want 2", len(blocks))
	}
	if blocks[0].Actor != "Ada" || blocks[0].ActorLine != 2 || blocks[0].StartLine != 2 || blocks[0].EndLine != 4 {
		t.Fatalf("first block = %#v", blocks[0])
	}
	if len(blocks[0].Items) != 2 || blocks[0].Items[0].Type != "Cloak" || blocks[0].Items[0].Line != 3 || blocks[0].Items[1].Line != 4 {
		t.Fatalf("first items = %#v", blocks[0].Items)
	}
	if blocks[1].Actor != "Banquo" || blocks[1].ActorLine != 6 || blocks[1].Items[0].Line != 7 {
		t.Fatalf("second block = %#v", blocks[1])
	}
}

func TestParseRejectsEmptyAndActorOnlyBlocks(t *testing.T) {
	for _, input := range []string{"", " \r\n\t", "Ada\n\nCloak\n"} {
		_, err := Parse(input)
		if err == nil {
			t.Fatalf("Parse(%q) succeeded", input)
		}
		var parseErr *ParseError
		if !errors.As(err, &parseErr) {
			t.Fatalf("Parse(%q) error %T, want ParseError", input, err)
		}
	}
	_, err := Parse("\nAda\nCloak\n\n")
	if err != nil {
		t.Fatal(err)
	}
}

func TestParseEntriesPreservesOrderAndBlockNumbers(t *testing.T) {
	entries, err := ParseEntries(strings.Join([]string{"Ada", "Cloak", "Hat", "", "Ada", "Cloak"}, "\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 || entries[0].Block != 1 || entries[2].Block != 2 || entries[2].ItemLine != 6 {
		t.Fatalf("entries = %#v", entries)
	}
}
