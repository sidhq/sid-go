package sid_test

import (
	"bufio"
	"encoding/json"
	"os"
	"testing"

	sid "github.com/sidhq/sid-go"
)

// TestAlyzeParity replays snippet calls recorded from sid-sdk's Rust core.
// Regenerate with internal/generate_parity_fixture.py when Alyze changes.
func TestAlyzeParity(t *testing.T) {
	t.Parallel()
	file, err := os.Open("testdata/alyze_parity.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	failures, count := 0, 0
	for scanner.Scan() {
		var test struct {
			Query, Content string
			Window, Stride int
			Language       sid.Language
			Want           sid.CharacterRange
		}
		if err := json.Unmarshal(scanner.Bytes(), &test); err != nil {
			t.Fatal(err)
		}
		count++
		got, err := sid.BM25SnippetWithStride(test.Query, test.Content, sid.SnippetOptions{
			WindowSize: test.Window,
			Stride:     test.Stride,
			Language:   test.Language,
		})
		if err != nil {
			t.Fatalf("%q in %q: %v", test.Query, test.Content, err)
		}
		if got != test.Want {
			failures++
			if failures <= 10 {
				t.Errorf("%q in %q (window %d, %s): got %v, want %v",
					test.Query, test.Content, test.Window, test.Language, got, test.Want)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if failures > 0 {
		t.Errorf("%d of %d cases differ from sid-sdk", failures, count)
	}
}
