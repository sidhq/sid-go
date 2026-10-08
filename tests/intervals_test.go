package sid_test

import (
	"math/rand"
	"testing"

	sid "github.com/sidhq/sid-go"
)

func TestInsertIntervalMergesOverlapsAndTouchingRanges(t *testing.T) {
	t.Parallel()
	tests := []struct {
		existing []sid.CharacterRange
		next     sid.CharacterRange
		expected []sid.CharacterRange
	}{
		{nil, sid.CharacterRange{5, 10}, []sid.CharacterRange{{5, 10}}},
		{[]sid.CharacterRange{{5, 10}}, sid.CharacterRange{8, 20}, []sid.CharacterRange{{5, 20}}},
		{[]sid.CharacterRange{{5, 10}}, sid.CharacterRange{10, 20}, []sid.CharacterRange{{5, 20}}},
		{[]sid.CharacterRange{{5, 10}}, sid.CharacterRange{20, 30}, []sid.CharacterRange{{5, 10}, {20, 30}}},
		{
			[]sid.CharacterRange{{5, 10}, {20, 30}},
			sid.CharacterRange{9, 21},
			[]sid.CharacterRange{{5, 30}},
		},
	}
	for _, test := range tests {
		actual, err := sid.InsertInterval(test.existing, test.next)
		if err != nil {
			t.Fatal(err)
		}
		if !rangesEqual(actual, test.expected) {
			t.Fatalf("InsertInterval(%v, %v) = %v, want %v", test.existing, test.next, actual, test.expected)
		}
	}
	if _, err := sid.InsertInterval(nil, sid.CharacterRange{5, 5}); err == nil {
		t.Fatal("empty interval did not fail")
	}
}

func TestIntervalPlanningInvariants(t *testing.T) {
	t.Parallel()
	random := rand.New(rand.NewSource(1))
	for range 500 {
		var ledger []sid.CharacterRange
		for range random.Intn(8) {
			start := random.Intn(3000)
			var err error
			ledger, err = sid.InsertInterval(
				ledger,
				sid.CharacterRange{start, start + 1 + random.Intn(400)},
			)
			if err != nil {
				t.Fatal(err)
			}
		}
		start := random.Intn(3000)
		span := sid.CharacterRange{start, start + 1 + random.Intn(800)}
		display, seen := sid.PlanSegments(span, ledger, 100)
		segments := append(append([]sid.CharacterRange(nil), display...), seen...)
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

func rangesEqual(left, right []sid.CharacterRange) bool {
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
