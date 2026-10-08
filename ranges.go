package sid

import (
	"fmt"
	"math"
	"strings"
	"unicode"
	"unicode/utf8"
)

// CharacterRange is a half-open range of Unicode code-point offsets.
//
// Go string byte offsets are not used by this SDK. For example, the range
// [0, 1] identifies the whole string "😀".
type CharacterRange [2]int

// NewCharacterRange constructs the half-open range [start, end).
func NewCharacterRange(start, end int) CharacterRange {
	return CharacterRange{start, end}
}

// Start returns the inclusive start offset.
func (r CharacterRange) Start() int { return r[0] }

// End returns the exclusive end offset.
func (r CharacterRange) End() int { return r[1] }

// RangeMode controls how partially out-of-bounds character ranges are handled.
type RangeMode string

const (
	// RangeModeLenient intersects a partially overlapping range with the
	// document. Empty, inverted, and wholly disjoint ranges still fail.
	RangeModeLenient RangeMode = "lenient"
	// RangeModeStrict requires the complete range to be within the document.
	RangeModeStrict RangeMode = "strict"
)

// InvalidCharacterRange reports a range that cannot identify a valid,
// non-empty document slice.
type InvalidCharacterRange struct {
	Message string
}

func (e *InvalidCharacterRange) Error() string { return e.Message }

// InvalidCharacterRangeError is the idiomatic Go spelling of
// InvalidCharacterRange.
type InvalidCharacterRangeError = InvalidCharacterRange

func validateRangeMode(mode RangeMode) (RangeMode, error) {
	switch mode {
	case RangeModeLenient, RangeModeStrict:
		return mode, nil
	default:
		return "", fmt.Errorf(
			"unsupported range mode %q; supported values: %q, %q",
			mode, RangeModeLenient, RangeModeStrict,
		)
	}
}

func resolveRange(charRange CharacterRange, documentLength int, mode RangeMode, documentID string) (CharacterRange, error) {
	start, end := charRange[0], charRange[1]
	prefix := fmt.Sprintf(
		"invalid character range %d:%d for document %q (%d characters): ",
		start, end, documentID, documentLength,
	)

	if start >= end {
		return CharacterRange{}, &InvalidCharacterRange{
			Message: fmt.Sprintf("%sstart %d must be less than end %d", prefix, start, end),
		}
	}
	if end <= 0 || start >= documentLength {
		return CharacterRange{}, &InvalidCharacterRange{
			Message: prefix + "the requested range does not overlap the document",
		}
	}
	if mode == RangeModeStrict {
		if start < 0 {
			return CharacterRange{}, &InvalidCharacterRange{
				Message: fmt.Sprintf("%sstart %d must be at least 0", prefix, start),
			}
		}
		if end > documentLength {
			return CharacterRange{}, &InvalidCharacterRange{
				Message: fmt.Sprintf("%send %d exceeds the document length", prefix, end),
			}
		}
		return charRange, nil
	}

	return CharacterRange{max(0, start), min(end, documentLength)}, nil
}

// codePointText slices a string by code-point offsets without materializing
// a []rune copy of the whole text.
type codePointText struct {
	text   string
	length int
}

func newCodePointText(text string) (codePointText, error) {
	if !utf8.ValidString(text) {
		return codePointText{}, fmt.Errorf("text must contain well-formed UTF-8")
	}
	return codePointText{text: text, length: utf8.RuneCountInString(text)}, nil
}

func (text codePointText) len() int { return text.length }

func (text codePointText) slice(start, end int) string {
	from := byteOffset(text.text, 0, start)
	return text.text[from:byteOffset(text.text, from, end-start)]
}

// byteOffset returns the byte index count code points after byte index from.
func byteOffset(text string, from, count int) int {
	for count > 0 {
		_, size := utf8.DecodeRuneInString(text[from:])
		from += size
		count--
	}
	return from
}

// ParseRenderedModelFacingID parses a bare model-facing ID or a ranged
// reference such as "004218#10:70". The returned range is nil for a bare ID.
//
// Surrounding whitespace and Unicode decimal digits are accepted in offsets.
// Bounds against a document are deliberately left to DocumentCache.
func ParseRenderedModelFacingID(reference string) (string, *CharacterRange, error) {
	if reference == "" {
		return "", nil, fmt.Errorf("invalid document reference %q: expected a non-empty string", reference)
	}
	hash := strings.IndexByte(reference, '#')
	if hash < 0 {
		return reference, nil, nil
	}
	if hash == 0 {
		return "", nil, fmt.Errorf(
			"invalid document reference %q: missing document id before '#'",
			reference,
		)
	}

	modelID := reference[:hash]
	parts := strings.Split(reference[hash+1:], ":")
	if len(parts) != 2 {
		return "", nil, malformedReferenceError(reference, modelID)
	}

	start, err := parseDecimalInteger(strings.TrimSpace(parts[0]))
	if err != nil {
		return "", nil, malformedReferenceError(reference, modelID)
	}
	end, err := parseDecimalInteger(strings.TrimSpace(parts[1]))
	if err != nil {
		return "", nil, malformedReferenceError(reference, modelID)
	}
	if start >= end {
		return "", nil, fmt.Errorf(
			"invalid document reference %q: range %d:%d is empty (start must be < end)",
			reference, start, end,
		)
	}

	charRange := CharacterRange{start, end}
	return modelID, &charRange, nil
}

func malformedReferenceError(reference, modelID string) error {
	return fmt.Errorf(
		"invalid document reference %q: expected '<doc_id>#<start>:<end>' with decimal integer character offsets, e.g. '%s#10:70'",
		reference, modelID,
	)
}

func parseDecimalInteger(value string) (int, error) {
	if value == "" {
		return 0, fmt.Errorf("empty integer")
	}

	negative := false
	if value[0] == '-' {
		negative = true
		value = value[1:]
		if value == "" {
			return 0, fmt.Errorf("empty integer")
		}
	}

	result := 0
	for _, char := range value {
		digit, ok := decimalDigitValue(char)
		if !ok {
			return 0, fmt.Errorf("not a decimal integer")
		}
		if result > (math.MaxInt-digit)/10 {
			return 0, fmt.Errorf("integer overflow")
		}
		result = result*10 + digit
	}
	if negative {
		result = -result
	}
	return result, nil
}

func decimalDigitValue(char rune) (int, bool) {
	if !unicode.Is(unicode.Nd, char) {
		return 0, false
	}

	// Unicode decimal-digit sets are contiguous runs of ten. Some styled
	// mathematical sets are adjacent, so modulo ten is intentional.
	first := char
	for first > 0 && unicode.Is(unicode.Nd, first-1) {
		first--
	}
	return int(char-first) % 10, true
}
