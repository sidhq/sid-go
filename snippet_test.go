package sid

import (
	"math"
	"math/bits"
	"sync"
	"testing"
)

func TestBM25SnippetParity(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		query, content string
		window, stride int
		language       Language
		expected       CharacterRange
	}{
		{
			name:     "phrase beats scattered terms",
			query:    "contingent fee agreement",
			content:  "contingent filler filler fee filler filler agreement pad pad pad contingent fee agreement tail tail tail",
			window:   6,
			stride:   2,
			language: LanguageEnglish,
			expected: CharacterRange{57, 94},
		},
		{
			name:     "legal fee calculation",
			query:    "expenses deducted before or after contingent fee calculated",
			content:  "Background procedural facts occupy this opening passage and do not discuss the disputed terms. Additional unrelated history appears here. Further, the hearing judge determined that Mr. Sanderson violated the provision through his failure to provide Ms. Ozel with adequate information regarding the expenses associated with the representation, whether such expenses would be deducted before or after the contingent fee is calculated, and by failing to document the agreement.",
			window:   24,
			stride:   5,
			language: LanguageEnglish,
			expected: CharacterRange{318, 473},
		},
		{
			name:     "legal fee agreement recommendation",
			query:    "fee agreement contingent recommendation reasonable settlement offer",
			content:  "The record begins with unrelated scheduling and jurisdictional matters. We agree with the Panel's conclusion that the fee under the Fee Agreement was not contingent on the outcome of the case, but rather, it was contingent on the Attorney's recommendation of a settlement offer which he deemed reasonable. The remaining section addresses sanctions.",
			window:   25,
			stride:   5,
			language: LanguageEnglish,
			expected: CharacterRange{132, 283},
		},
		{
			name:     "german stopword",
			query:    "alpha und beta",
			content:  "alpha beta und x x alpha und beta x x x x",
			window:   4,
			stride:   1,
			language: LanguageGerman,
			expected: CharacterRange{0, 16},
		},
		{
			name:     "generic keeps stopword",
			query:    "alpha und beta",
			content:  "alpha beta und x x alpha und beta x x x x",
			window:   4,
			stride:   1,
			language: LanguageGeneric,
			expected: CharacterRange{17, 33},
		},
		{
			name:     "astral unicode",
			query:    "target",
			content:  "😀 zero one café target three four",
			window:   3,
			stride:   1,
			language: LanguageEnglish,
			expected: CharacterRange{7, 22},
		},
		{
			name:     "combining mark",
			query:    "target",
			content:  "e\u0301 zero one target three four",
			window:   3,
			stride:   1,
			language: LanguageEnglish,
			expected: CharacterRange{3, 18},
		},
		{
			name:     "cjk segmentation",
			query:    "target",
			content:  "日本語 中文 한국어 target more words here",
			window:   3,
			stride:   1,
			language: LanguageEnglish,
			expected: CharacterRange{5, 17},
		},
		{
			name:     "final window",
			query:    "last",
			content:  "zero one two three four five last",
			window:   3,
			stride:   20,
			language: LanguageEnglish,
			expected: CharacterRange{19, 33},
		},
		{
			name:     "stopword only",
			query:    "the and",
			content:  "one two three four five six seven eight",
			window:   3,
			stride:   1,
			language: LanguageEnglish,
			expected: CharacterRange{0, 13},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			actual, err := BM25SnippetWithStride(test.query, test.content, SnippetOptions{
				WindowSize: test.window,
				Stride:     test.stride,
				Language:   test.language,
			})
			if err != nil {
				t.Fatal(err)
			}
			if actual != test.expected {
				t.Fatalf("got %v, want %v; text %q", actual, test.expected, runeSlice(test.content, actual))
			}
		})
	}
}

func TestBM25SnippetShortAndPunctuationDocuments(t *testing.T) {
	t.Parallel()
	for _, content := range []string{"", "!!! ...", "one two"} {
		span, err := BM25SnippetWithStride("query", content, SnippetOptions{
			WindowSize: 3,
			Stride:     1,
		})
		if err != nil {
			t.Fatal(err)
		}
		expected := CharacterRange{0, len([]rune(content))}
		if span != expected {
			t.Fatalf("content %q: got %v, want %v", content, span, expected)
		}
	}
}

func TestBM25SnippetValidation(t *testing.T) {
	t.Parallel()
	if _, err := BM25SnippetWithStride("q", "text", SnippetOptions{WindowSize: -1, Stride: 1}); err == nil {
		t.Fatal("negative window size did not fail")
	}
	if _, err := BM25SnippetWithStride("q", "text", SnippetOptions{WindowSize: 2, Stride: -1}); err == nil {
		t.Fatal("negative stride did not fail")
	}
	if _, err := BM25SnippetWithStride("q", "text", SnippetOptions{Language: "klingon"}); err == nil {
		t.Fatal("unknown language did not fail")
	}
	if _, err := BM25SnippetWithStride("q", string([]byte{0xff})); err == nil {
		t.Fatal("malformed UTF-8 did not fail")
	}
	if bits.UintSize == 64 {
		tooLarge := uint64(math.MaxUint32) + 1
		if _, err := BM25SnippetWithStride("q", "text", SnippetOptions{
			WindowSize: int(tooLarge),
			Stride:     1,
		}); err == nil {
			t.Fatal("window size larger than uint32 did not fail")
		}
	}
}

func TestEverySupportedLanguage(t *testing.T) {
	t.Parallel()
	for _, language := range SupportedLanguages {
		if _, err := BM25SnippetWithStride("target", "some target text", SnippetOptions{
			Language: language,
		}); err != nil {
			t.Errorf("%s: %v", language, err)
		}
	}
}

func TestBM25SnippetConcurrentDeterminism(t *testing.T) {
	content := wordsForTest(500)
	expected, err := BM25SnippetWithStride("w0400 w0401", content)
	if err != nil {
		t.Fatal(err)
	}

	const workers = 16
	var wait sync.WaitGroup
	errors := make(chan error, workers)
	for range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for range 50 {
				actual, err := BM25SnippetWithStride("w0400 w0401", content)
				if err != nil {
					errors <- err
					return
				}
				if actual != expected {
					errors <- &spanMismatch{actual: actual, expected: expected}
					return
				}
			}
		}()
	}
	wait.Wait()
	close(errors)
	for err := range errors {
		t.Fatal(err)
	}
}

type spanMismatch struct {
	actual, expected CharacterRange
}

func (err *spanMismatch) Error() string {
	return "snippet selection was nondeterministic"
}

func runeSlice(text string, span CharacterRange) string {
	return string([]rune(text)[span[0]:span[1]])
}

func wordsForTest(count int) string {
	result := ""
	for i := range count {
		if i > 0 {
			result += " "
		}
		result += "w" + leftPad(i, 4)
	}
	return result
}

func leftPad(value, width int) string {
	digits := "0000000000"
	text := ""
	if value == 0 {
		text = "0"
	} else {
		for value > 0 {
			text = string(rune('0'+value%10)) + text
			value /= 10
		}
	}
	if missing := width - len(text); missing > 0 {
		text = digits[:missing] + text
	}
	return text
}
