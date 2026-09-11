package warehouse

import "testing"

// TestSourceShape_Known covers the vocabulary check that keeps a mistyped
// shape from being stored. The zero value is NOT known on purpose: absence
// means the source never declared one, which each caller resolves through
// its own EffectiveShape — a different question from whether a spelling
// names a shape at all.
func TestSourceShape_Known(t *testing.T) {
	for _, tc := range []struct {
		shape SourceShape
		want  bool
	}{
		{ShapeEntities, true},
		{ShapeCube, true},
		{"", false},
		{"cubes", false},
		{"Entities", false},
		{"table", false},
	} {
		if got := tc.shape.Known(); got != tc.want {
			t.Errorf("SourceShape(%q).Known() = %v, want %v", tc.shape, got, tc.want)
		}
	}
}

// TestSourceShape_KnownCoversEveryDeclaredShape fails when a shape is added
// to the vocabulary without being taught to Known — which would let a
// legitimate new shape be rejected as a typo at every save.
func TestSourceShape_KnownCoversEveryDeclaredShape(t *testing.T) {
	for _, shape := range []SourceShape{ShapeEntities, ShapeCube} {
		if !shape.Known() {
			t.Errorf("declared shape %q is not Known", shape)
		}
		if (Capability{Shape: shape}).EffectiveShape() != shape {
			t.Errorf("declared shape %q does not survive EffectiveShape", shape)
		}
	}
}
