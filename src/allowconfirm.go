package src

import (
	"bufio"
	"crypto/rand"
	"fmt"
	"math/big"
	"os"
	"strings"
)

const codeDigits = 6

// confirmOnTerminal shows a one-time code on /dev/tty and reads it back from /dev/tty. Output to the user's
// terminal never reaches a tool that runs the command, so only a person at the terminal can answer.
func confirmOnTerminal(rule string) error {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return errAllowNeedsTerminal
	}
	defer tty.Close()

	code, err := oneTimeCode()
	if err != nil {
		return fmt.Errorf("making a confirmation code: %w", err)
	}

	fmt.Fprintf(tty, "Approve one %s? Type %s to confirm: ", rule, code)
	answer, err := bufio.NewReader(tty).ReadString('\n')
	if err != nil || strings.TrimSpace(answer) != code {
		return errAllowNotConfirmed
	}
	return nil
}

func oneTimeCode() (string, error) {
	const codeRange = 1_000_000
	n, err := rand.Int(rand.Reader, big.NewInt(codeRange))
	if err != nil {
		return "", fmt.Errorf("reading randomness: %w", err)
	}
	return fmt.Sprintf("%0*d", codeDigits, n.Int64()), nil
}
