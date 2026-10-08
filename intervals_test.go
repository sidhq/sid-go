package sid

import (
	"math/rand"
	"testing"
)

func TestInsertIntervalMergesOverlapsAndTouchingRanges(t *testing.T) {
	t.Parallel()
	tests := []struct {
		existing []CharacterRange
		next     CharacterRange
		expected []CharacterRange
	}{
		{nil, CharacterRange{5, 10}, []CharacterRange{{5, 10}}},
		{[]CharacterRange{{5, 10}}, CharacterRange{8, 20}, []CharacterRange{{5, 20}}},
		{[]CharacterRange{{5, 10}}, CharacterRange{10, 20}, []CharacterRange{{5, 20}}},
		{[]CharacterRange{{5, 10}}, CharacterRange{20, 30}, []CharacterRange{{5, 10}, {20, 30}}},
		{
			[]CharacterRange{{5, 10}, {20, 30}},
			CharacterRange{9, 21},
			[]CharacterRange{{5, 30}},
		},
	}
	for _, test := range tests {
		actual, err := InsertInterval(test.existing, test.next)
		if err != nil {
			t.Fatal(err)
		}
		if !rangesEqual(actual, test.expected) {
			t.Fatalf("InsertInterval(%v, %v) = %v, want %v", test.existing, test.next, actual, test.expected)
		}
	}
	if _, err := InsertInterval(nil, CharacterRange{5, 5}); err == nil {
		t.Fatal("empty interval did not fail")
	}
}

func TestIntervalPlanningInvariants(t *testing.T) {
	t.Parallel()
	random := rand.New(rand.NewSource(1))
	for range 500 {
		var ledger []CharacterRange
		for range random.Intn(8) {
			start := random.Intn(3000)
			var err error
			ledger, err = InsertInterval(
				ledger,
				CharacterRange{start, start + 1 + random.Intn(400)},
			)
			if err != nil {
				t.Fatal(err)
			}
		}
		start := random.Intn(3000)
		span := CharacterRange{start, start + 1 + random.Intn(800)}
		display, seen := PlanSegments(span, ledger, 100)
		segments := append(append([]CharacterRange(nil), display...), seen...)
		for i := 0; i < len(segments); i++ {
			for j := i + 1; j < len(segments); j++ {
				if segments[j][0] < segments[i][0] {
					segments[i], segments[j] = segments[j], segments[i]
				}
			}
		}
		cursor := span[0]
		for _, segment := range segments {
			if segment[0] != cursor || segment[0] >= segment[1] {
				t.Fatalf("segments %v do not tile %v", segments, span)
			}
			cursor = segment[1]
		}
		if cursor != span[1] {
			t.Fatalf("segments %v end at %d, want %d", segments, cursor, span[1])
		}
		for _, segment := range seen {
			if segment[1]-segment[0] < 100 {
				t.Fatalf("masked short segment %v", segment)
			}
		}
	}
}

func rangesEqual(left, right []CharacterRange) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
