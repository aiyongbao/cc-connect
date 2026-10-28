package core

// recentTimelineEntriesByKind keeps each kind's latest entries independently.
// Scanning backwards finds each window; compacting forwards preserves event
// order and avoids grouping all reasoning ahead of all tool calls.
func recentTimelineEntriesByKind(items []ProgressCardEntry) []ProgressCardEntry {
	counts := make(map[ProgressCardEntryKind]int)
	keep := make([]bool, len(items))
	for i := len(items) - 1; i >= 0; i-- {
		kind := items[i].Kind
		counts[kind]++
		keep[i] = counts[kind] <= timelineMaxEntriesPerKind
	}
	retained := items[:0]
	for i, item := range items {
		if keep[i] {
			retained = append(retained, item)
		}
	}
	return retained
}
