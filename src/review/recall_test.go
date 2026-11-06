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
		{"keyword far from the line", []Finding{{File: "db/query.go", Line: 90, What: "SQL injection via name"}}, []string{"injection"}, []string{"off-by-one"}, 0},
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

func TestScoreBlocking_CleanCaseCountsEveryFindingAsSpurious(t *testing.T) {
	got := ScoreBlocking(nil, []Finding{{File: "a.go", Line: 1}, {File: "b.go", Line: 2}})
	require.Equal(t, 2, got.Spurious)
}
