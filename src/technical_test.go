package src

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTechnicalSequenceMessagesAvoidStatementSeparators(t *testing.T) {
	data, err := os.ReadFile("../TECHNICAL.md")
	require.NoError(t, err)
	inDiagram, sequence := false, false
	sequences := 0
	for lineNumber, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "```mermaid" {
			inDiagram = true
			continue
		}
		if line == "```" {
			inDiagram, sequence = false, false
			continue
		}
		if !inDiagram {
			continue
		}
		if line == "sequenceDiagram" {
			sequence = true
			sequences++
		}
		if sequence {
			if _, message, ok := strings.Cut(line, ":"); ok {
				require.NotContains(t, message, ";", "TECHNICAL.md:%d: use words instead of Mermaid's semicolon statement separator", lineNumber+1)
			}
		}
	}
	require.Positive(t, sequences)
}
