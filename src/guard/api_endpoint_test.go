package guard

import "testing"

func TestAPIEndpointRejectsTrailingAmbiguousFlags(t *testing.T) {
	for _, args := range [][]string{
		{"repos/{owner}/{repo}/pulls", "-if", "title=x"},
		{"repos/{owner}/{repo}/pulls", "--unknown"},
		{"repos/{owner}/{repo}/pulls", "second"},
	} {
		if _, ok := apiEndpoint(args); ok {
			t.Errorf("accepted ambiguous args: %q", args)
		}
	}
}
