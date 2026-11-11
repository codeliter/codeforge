//nolint:err113 // dynamic error messages are okay in tests
package src

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTicketReviewLoop(t *testing.T) {
	type round struct {
		report string
		passed bool
		err    error
	}
	cases := []struct {
		name    string
		rounds  []round
		fixErr  error
		gates   int
		fixes   int
		message string
	}{
		{"passes first time", []round{{"- r: PASS\n", true, nil}}, nil, 1, 0, "review passed"},
		{"fixed after one round", []round{{"Blocking:\n- a.go:3 nil deref\n", false, nil}, {"- r: PASS\n", true, nil}}, nil, 2, 1, "review passed"},
		{"still blocking after the limit", []round{{"Blocking:\n- x\n", false, nil}, {"Blocking:\n- x\n", false, nil}}, nil, maxGateRounds, maxGateRounds - 1, "still blocking after 2 rounds"},
		{"gate runs used up", []round{{"Still blocking from the last run:\n- x\n", false, fmt.Errorf("%w: 2 failed runs", errGateCapped)}}, nil, 1, 0, "runs on this branch are used up"},
		{"reviewers unreachable", []round{{"", false, errors.New("no reviewer answered")}}, nil, 1, 0, "review could not run"},
		{"partial report then error", []round{{"- r: no verdict (timeout)\n", false, errors.New("gate: timeout")}}, nil, 1, 0, "review could not run"},
		{"fix turn fails", []round{{"Blocking:\n- x\n", false, nil}}, errors.New("engine exited"), 1, 1, "fixing the findings failed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gates, fixes := 0, 0
			var prompts []string
			gate := func(context.Context) (string, bool, error) {
				r := c.rounds[gates]
				gates++
				return r.report, r.passed, r.err
			}
			fix := func(_ context.Context, prompt string) error {
				fixes++
				prompts = append(prompts, prompt)
				return c.fixErr
			}
			var out strings.Builder
			ticketReviewLoop(t.Context(), gate, fix, &out)
			require.Equal(t, c.gates, gates)
			require.Equal(t, c.fixes, fixes)
			require.Contains(t, out.String(), c.message)
			for _, p := range prompts {
				require.Contains(t, p, "Fix every Blocking finding")
				require.Contains(t, p, "<gate>\nBlocking:")
				require.Contains(t, p, "Do not open the PR")
			}
		})
	}
}
