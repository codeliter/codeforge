package review

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestScoreBlocking(t *testing.T) {
	defects := []Defect{
		{Name: "off-by-one", File: "page.go", Lo: 10, Hi: 12, Keywords: []string{"last item"}},
		{Name: "injection", File: "db/query.go", Lo: 5, Hi: 5, Keywords: []string{"sql injection"}},
	}
	tests := []struct {
		name     string
		blocking []Finding
		found    []string
		missed   []string
		spurious int
	}{
		{"line near the defect", []Finding{{File: "page.go", Line: 14, What: "x"}}, []string{"off-by-one"}, []string{"injection"}, 0},
		{"keyword some lines from the defect", []Finding{{File: "db/query.go", Line: 16, What: "SQL injection via name"}}, []string{"injection"}, []string{"off-by-one"}, 0},
		{"keyword far from the defect is not evidence", []Finding{{File: "db/query.go", Line: 90, What: "SQL injection via name"}}, nil, []string{"off-by-one", "injection"}, 1},
		{"right words, wrong file", []Finding{{File: "other.go", Line: 11, What: "last item lost"}}, nil, []string{"off-by-one", "injection"}, 1},
		{"path is normalised", []Finding{{File: "./db/query.go", Line: 5}}, []string{"injection"}, []string{"off-by-one"}, 0},
		{"nothing reported", nil, nil, []string{"off-by-one", "injection"}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ScoreBlocking(defects, tt.blocking)
			require.Equal(t, tt.found, got.Found)
			require.Equal(t, tt.missed, got.Missed)
			require.Equal(t, tt.spurious, got.Spurious)
		})
	}
}

func TestScoreBlocking_OneFindingCreditsOneDefect(t *testing.T) {
	defects := []Defect{
		{Name: "min", File: "h.go", Lo: 382, Hi: 382, Keywords: []string{"minimum", ">="}},
		{Name: "cap", File: "h.go", Lo: 388, Hi: 388, Keywords: []string{"cap", ">="}},
		{Name: "flip", File: "h.go", Lo: 248, Hi: 248, Keywords: []string{">="}},
	}
	t.Run("overlapping line windows go to the closer defect", func(t *testing.T) {
		got := ScoreBlocking(defects, []Finding{{File: "h.go", Line: 383, What: "rejects the minimum"}})
		require.Equal(t, []string{"min"}, got.Found)
		require.Equal(t, []string{"cap", "flip"}, got.Missed)
	})
	t.Run("a shared word in the same file does not credit a second defect", func(t *testing.T) {
		got := ScoreBlocking(defects, []Finding{{File: "h.go", Line: 248, What: "should be >= here"}})
		require.Equal(t, []string{"flip"}, got.Found)
	})
	t.Run("a finding outside every window goes to the defect whose words it uses most", func(t *testing.T) {
		got := ScoreBlocking(defects, []Finding{{File: "h.go", Line: 395, What: "the cap is wrong, use >="}})
		require.Equal(t, []string{"cap"}, got.Found)
	})
	t.Run("a line window ties go to the defect whose words the finding uses", func(t *testing.T) {
		got := ScoreBlocking(defects, []Finding{{File: "h.go", Line: 385, What: "the cap keeps one fewer"}})
		require.Equal(t, []string{"cap"}, got.Found)
		got = ScoreBlocking(defects, []Finding{{File: "h.go", Line: 385, What: "rejects the minimum"}})
		require.Equal(t, []string{"min"}, got.Found)
	})
	t.Run("a finding next to one defect that describes its neighbour is credited to the neighbour", func(t *testing.T) {
		got := ScoreBlocking(defects, []Finding{{File: "h.go", Line: 384, What: "the cap keeps one fewer"}})
		require.Equal(t, []string{"cap"}, got.Found)
		got = ScoreBlocking(defects, []Finding{{File: "h.go", Line: 386, What: "rejects the minimum"}})
		require.Equal(t, []string{"min"}, got.Found)
	})
	t.Run("pointing at the lines beats a neighbour's word", func(t *testing.T) {
		got := ScoreBlocking(defects, []Finding{{File: "h.go", Line: 388, What: "the minimum is checked wrongly"}})
		require.Equal(t, []string{"cap"}, got.Found)
	})
	t.Run("operators and numbers are not evidence", func(t *testing.T) {
		got := ScoreBlocking(defects, []Finding{{File: "h.go", Line: 300, What: "x >= y is wrong"}})
		require.Empty(t, got.Found)
		require.Equal(t, 1, got.Spurious)
	})
	t.Run("two equal fits are a guess and credit nothing", func(t *testing.T) {
		got := ScoreBlocking(defects, []Finding{{File: "h.go", Line: 385, What: "something is off"}})
		require.Empty(t, got.Found)
		require.Equal(t, 1, got.Spurious)
	})
	t.Run("two findings for the same defect are not spurious", func(t *testing.T) {
		got := ScoreBlocking(defects, []Finding{{File: "h.go", Line: 382}, {File: "h.go", Line: 381}})
		require.Equal(t, []string{"min"}, got.Found)
		require.Zero(t, got.Spurious)
	})
}

func TestScoreBlocking_CleanCaseCountsEveryFindingAsSpurious(t *testing.T) {
	got := ScoreBlocking(nil, []Finding{{File: "a.go", Line: 1}, {File: "b.go", Line: 2}})
	require.Equal(t, 2, got.Spurious)
}

func TestContainsWord(t *testing.T) {
	tests := []struct {
		text, keyword string
		want          bool
	}{
		{"the cap keeps one fewer", "cap", true},
		{"the path escapes", "cap", false},
		{"the recap omits a field", "cap", false},
		{"cap", "cap", true},
		{"a cap, then more", "cap", true},
		{"preserve the order", "reserve", false},
		{"the reserve is lost", "reserve", true},
		{"uses max(a, b) here", "max(", true},
		{"calls min(a)", "max(", false},
		{"v.err is ignored", "v.err", true},
		{"the capacity", "cap", false},
		{"a border and an order", "order", true},
		{"a border only", "order", false},
	}
	for _, tt := range tests {
		require.Equal(t, tt.want, containsWord(tt.text, tt.keyword), "%q in %q", tt.keyword, tt.text)
	}
}
