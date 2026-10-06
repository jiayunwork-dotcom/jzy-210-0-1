package store

import (
	"balancing/internal/history"
	"balancing/internal/vec"
)

// snapshotHistory renders the currently available per-speed influence
// matrices in the machine's external frame, for embedding in a reuse job's
// creation event.
func snapshotHistory(m *Machine, hist *history.State) map[int][][]vec.Polar {
	out := map[int][][]vec.Polar{}
	for _, speed := range m.Speeds {
		e, ok := hist.EntryAt(speed)
		if !ok {
			continue
		}
		out[speed] = matrixToExternal(m, e.H)
	}
	return out
}
