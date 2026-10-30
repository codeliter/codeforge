package review

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseVerdict(t *testing.T) {
	tests := []struct {
		name    string
		out     string
		want    string
		wantErr bool
	}{
		{"pass", "Looks good.\n```json\n{\"verdict\":\"pass\",\"summary\":\"ok\",\"blocking\":[],\"should\":[]}\n```", VerdictPass, false},
		{"fail", "```json\n{\"verdict\":\"fail\",\"blocking\":[{\"file\":\"a.go\",\"line\":3,\"what\":\"nil deref\"}]}\n```", VerdictFail, false},
		{"pass with blocking is a fail", `{"verdict":"pass","blocking":[{"file":"a.go","line":1,"what":"x"}]}`, VerdictFail, false},
		{"last block wins", `{"verdict":"pass"} later {"verdict":"fail","blocking":[{"file":"a","line":1,"what":"x"}]}`, VerdictFail, false},
		{"pretty printed", "Done.\n```json\n{\n  \"verdict\": \"pass\",\n  \"summary\": \"ok {fine}\",\n  \"blocking\": [],\n  \"should\": []\n}\n```\n", VerdictPass, false},
		{"braces and quotes in strings", `{"verdict":"fail","blocking":[{"file":"a.go","line":1,"what":"use \"}\" carefully {"}]}`, VerdictFail, false},
		{"malformed final block does not fall back", `{"verdict":"pass"} then {"verdict":"pass",}`, "", true},
		{"text after the block", `{"verdict":"pass"} trailing words`, "", true},
		{"no verdict", "I could not finish.", "", true},
		{"unknown verdict", `{"verdict":"maybe"}`, "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseVerdict(tt.out)
			if tt.wantErr {
				require.ErrorIs(t, err, ErrNoVerdict)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got.Verdict)
		})
	}
}
