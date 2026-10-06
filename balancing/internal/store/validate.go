package store

import "math"

// ValidateMachine checks machine configuration. A nil entry in holeLayout
// means "arbitrary angle allowed"; a present entry with fewer than 3 holes is
// rejected with a per-plane field error.
func ValidateMachine(name string, planeCount, pointCount int, speeds []int, holeLayout []*HoleLayoutIn, phaseDirection string, referenceDeg float64, pointWeights []float64, strategy string) *ValidationError {
	var fes []FieldError
	if name == "" {
		fes = append(fes, FieldError{"name", "required", "machine name is required"})
	}
	if planeCount < 1 || planeCount > 2 {
		fes = append(fes, FieldError{"plane_count", "out_of_range", "correction plane count must be 1 or 2"})
	}
	if pointCount < 1 || pointCount > 4 {
		fes = append(fes, FieldError{"point_count", "out_of_range", "measurement point count must be between 1 and 4"})
	}
	if len(speeds) == 0 {
		fes = append(fes, FieldError{"speeds", "required", "at least one working speed is required"})
	}
	seen := map[int]bool{}
	for i, s := range speeds {
		if s <= 0 {
			fes = append(fes, FieldError{fieldName("speeds", i), "out_of_range", "speed must be positive rpm"})
		}
		if seen[s] {
			fes = append(fes, FieldError{fieldName("speeds", i), "duplicate", "speeds must be unique"})
		}
		seen[s] = true
	}
	if phaseDirection != "against_rotation" && phaseDirection != "with_rotation" {
		fes = append(fes, FieldError{"phase_direction", "invalid", `must be "against_rotation" or "with_rotation"`})
	}
	if math.IsNaN(referenceDeg) || math.IsInf(referenceDeg, 0) {
		fes = append(fes, FieldError{"reference_deg", "invalid", "reference angle must be finite"})
	}
	if strategy != "" && strategy != "latest" && strategy != "sliding_weighted" && strategy != "uncertainty_weighted" {
		fes = append(fes, FieldError{"fusion_strategy", "invalid", `must be "latest", "sliding_weighted" or "uncertainty_weighted"`})
	}
	if planeCount >= 1 && planeCount <= 2 {
		for p := 0; p < planeCount; p++ {
			if p < len(holeLayout) && holeLayout[p] != nil && holeLayout[p].count < 3 {
				fes = append(fes, FieldError{fieldName("hole_layout", p), "too_few_holes", "a fixed-hole plane needs at least 3 equiangular holes"})
			}
		}
	}
	for i, w := range pointWeights {
		if math.IsNaN(w) || math.IsInf(w, 0) || w <= 0 {
			fes = append(fes, FieldError{fieldName("point_weights", i), "invalid", "point weight must be finite and positive"})
		}
	}
	if len(fes) > 0 {
		return &ValidationError{Fields: fes}
	}
	return nil
}

// HoleLayoutIn is the validation view of a plane's hole pattern.
type HoleLayoutIn struct {
	count        int
	firstHoleDeg float64
}

// NewHoleLayoutIn builds a validation input (unexported fields via helper).
func NewHoleLayoutIn(count int, firstHoleDeg float64) *HoleLayoutIn {
	return &HoleLayoutIn{count: count, firstHoleDeg: firstHoleDeg}
}

func fieldName(base string, i int) string { return base + "[" + itoa(i) + "]" }

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [20]byte
	n := len(b)
	for i > 0 {
		n--
		b[n] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		n--
		b[n] = '-'
	}
	return string(b[n:])
}
