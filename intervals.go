package sid

import (
	"fmt"
	"slices"
)

// InsertInterval inserts a range into sorted, disjoint ranges and merges all
// overlapping or touching ranges.
func InsertInterval(intervals []CharacterRange, newRange CharacterRange) ([]CharacterRange, error) {
	a, b := newRange[0], newRange[1]
	if a >= b {
		return nil, fmt.Errorf("cannot record an empty interval %v", newRange)
	}

	out := make([]CharacterRange, 0, len(intervals)+1)
	placed := false
	for _, interval := range intervals {
		x, y := interval[0], interval[1]
		if y < a || x > b {
			if x > b && !placed {
				out = append(out, CharacterRange{a, b})
				placed = true
			}
			out = append(out, interval)
			continue
		}
		a = min(a, x)
		b = max(b, y)
	}
	if !placed {
		out = append(out, CharacterRange{a, b})
	}
	slices.SortFunc(out, func(left, right CharacterRange) int {
		return left[0] - right[0]
	})
	return out, nil
}

// Overlap returns the non-empty intersection of two ranges.
func Overlap(left, right CharacterRange) (CharacterRange, bool) {
	start := max(left[0], right[0])
	end := min(left[1], right[1])
	if start >= end {
		return CharacterRange{}, false
	}
	return CharacterRange{start, end}, true
}

// Overlaps returns the parts of span covered by intervals, in interval order.
func Overlaps(span CharacterRange, intervals []CharacterRange) []CharacterRange {
	result := make([]CharacterRange, 0, len(intervals))
	for _, interval := range intervals {
		if intersection, ok := Overlap(span, interval); ok {
			result = append(result, intersection)
		}
	}
	return result
}

// PlanSegments partitions span into text to display and text to mask. Seen
// stretches shorter than minSeenOverlap are displayed again.
func PlanSegments(span CharacterRange, seen []CharacterRange, minSeenOverlap int) (display, masked []CharacterRange) {
	appendDisplay := func(next CharacterRange) {
		if len(display) > 0 && display[len(display)-1][1] == next[0] {
			display[len(display)-1][1] = next[1]
			return
		}
		display = append(display, next)
	}

	cursor := span[0]
	for _, intersection := range Overlaps(span, seen) {
		x, y := intersection[0], intersection[1]
		if x > cursor {
			appendDisplay(CharacterRange{cursor, x})
		}
		if y-x >= minSeenOverlap {
			masked = append(masked, intersection)
		} else {
			appendDisplay(intersection)
		}
		cursor = y
	}
	if cursor < span[1] {
		appendDisplay(CharacterRange{cursor, span[1]})
	}
	return display, masked
}
