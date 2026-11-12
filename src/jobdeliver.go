package src

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/codeliter/codeforge/src/jobs"
)

// deliverWhenClosed posts the result back into the job's session as a new turn, once nobody has the session open and
// no waiter in it has taken the result already. The agent then continues without being prompted.
// It runs under the job's runner lock, so one runner at a time posts; it is marked delivered only once the turn went in,
// and a failed post is tried again, here and by later runners, up to maxDeliveryTries in all.
func deliverWhenClosed(ctx context.Context, home string, st jobs.Store, j *jobs.Job) {
	end := j.Finished.Add(deliveryWait)
	for j.DeliveryTries < maxDeliveryTries && time.Now().Before(end) {
		if st.Delivered(j.ID) {
			return
		}
		// The conversation itself says whether the result went in: a runner that died after posting never posts twice.
		if resultInSession(j) {
			st.MarkDelivered(j.ID)
			return
		}
		// One post at a time into a session: two jobs that end together never write it at once.
		unlockSession, err := st.LockSession(j.Workspace, j.Session)
		if err != nil || sessionOpen(j.Workspace, j.Session) || st.Delivered(j.ID) || resultInSession(j) {
			if unlockSession != nil {
				unlockSession()
			}
			time.Sleep(deliveryPoll)
			continue
		}
		j.DeliveryTries++
		if j.DeliveryToken == "" {
			j.DeliveryToken = jobs.NewToken()
		}
		// Saved before the post, so a runner that dies after it finds this post by its token.
		if st.Save(j) != nil {
			unlockSession()
			time.Sleep(deliveryPoll)
			continue
		}
		fmt.Printf("%s posting the result into session %s\n", time.Now().Format(time.RFC3339), j.Session)
		started, err := postToSession(ctx, home, j)
		unlockSession()
		// Delivered when the conversation holds the result, not when the engine ran: one that stopped before saving the
		// turn is tried again, and one that saved it is never posted twice, whatever its exit.
		if resultInSession(j) {
			st.MarkDelivered(j.ID)
			if err != nil {
				fmt.Println("the turn that took the result ended with an error:", err)
			}
			return
		}
		if started {
			fmt.Println("the engine ran but the result is not in the conversation:", err)
		} else {
			fmt.Println("could not post the result back:", err)
		}
		time.Sleep(deliveryPoll)
	}
}

// deliveryMark ends a posted result: a random token no other text in the conversation carries, so finding it means this
// post went in.
func deliveryMark(j *jobs.Job) string {
	return "(codeforge delivery " + j.DeliveryToken + ")"
}

// resultInSession reports whether the session's saved conversation holds the job's posted result, found by its token.
func resultInSession(j *jobs.Job) bool {
	if j.DeliveryToken == "" {
		return false // never posted
	}
	f, err := os.Open(filepath.Join(j.Workspace, ".empryo", "sessions", j.Session, "session.jsonl"))
	if err != nil {
		return false
	}
	defer f.Close() //nolint:errcheck // read only
	needle := []byte(deliveryMark(j))
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 64<<20) //nolint:mnd // a line holds a whole turn
	for sc.Scan() {
		if bytes.Contains(sc.Bytes(), needle) {
			return true
		}
	}
	return false
}

// runnerStartGrace is how long a new job's runner may take to start before another is started for it.
const runnerStartGrace = 30 * time.Second

// maxDeliveryTries bounds how often a result is posted back before it is left for the resume notice and job show.
const maxDeliveryTries = 5

// needsDelivery reports a finished job whose result still has to be posted back.
func needsDelivery(st jobs.Store, j *jobs.Job) bool {
	return j.State.Final() && j.Session != "" && !st.Delivered(j.ID) && j.DeliveryTries < maxDeliveryTries &&
		time.Since(j.Finished) < deliveryWait
}

// postToSession runs one headless turn in the job's session with its result as the prompt, in the session's own mode.
// started reports whether the engine ran: from then on the turn is in the conversation, whatever its exit.
func postToSession(ctx context.Context, home string, j *jobs.Job) (started bool, _ error) {
	if err := os.Chdir(j.Workspace); err != nil {
		return false, fmt.Errorf("opening the session's workspace: %w", err)
	}
	args := []string{"--session", j.Session}
	mode, _, err := restoreMode(ctx, home, "", args)
	if err != nil {
		return false, err
	}
	bin, err := findEmpryo(filepath.Join(home, ".empryo"))
	if err != nil {
		return false, err
	}
	prompt := j.Result() + "\nThis job was scheduled in this conversation. Continue the task that was waiting for it.\n" +
		deliveryMark(j)
	argv, env, err := launchCommand(ctx, home, bin, mode, append(args, "--headless", prompt))
	if err != nil {
		return false, err
	}
	// Checked again as late as possible: a person who opened the session meanwhile gets the result from the resume notice.
	if sessionOpen(j.Workspace, j.Session) {
		return false, errSessionReopened
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) //nolint:gosec // codeforge's own engine command
	cmd.Env, cmd.Stdout, cmd.Stderr = slices.Concat(env, []string{noAskEnv + "=1"}), os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return false, fmt.Errorf("starting the engine: %w", err)
	}
	return true, cmd.Wait() //nolint:wrapcheck // the engine's exit
}

type sessionRef struct {
	id, workspace string
	pid           int
}

// callingSession finds the Empryo session this command runs in: the one whose writer lock names one of this process's
// ancestors, in dir or a directory above it. Empryo gives its shell no session id, but its own process is our ancestor.
func callingSession(ctx context.Context, dir string) (sessionRef, bool) {
	ancestors := map[int]bool{}
	for pid, n := os.Getppid(), 0; pid > 1 && n < 32; n++ {
		ancestors[pid] = true
		out, err := exec.CommandContext(ctx, "ps", "-o", "ppid=", "-p", strconv.Itoa(pid)).Output()
		if err != nil {
			break
		}
		if pid, err = strconv.Atoi(strings.TrimSpace(string(out))); err != nil {
			break
		}
	}
	for d := dir; ; d = filepath.Dir(d) {
		entries, _ := os.ReadDir(filepath.Join(d, ".empryo", "sessions")) //nolint:errcheck // none is none
		for _, e := range entries {
			if pid := lockPID(filepath.Join(d, ".empryo", "sessions", e.Name())); ancestors[pid] && safeSessionID(e.Name()) {
				return sessionRef{id: e.Name(), workspace: d, pid: pid}, true
			}
		}
		if filepath.Dir(d) == d {
			return sessionRef{}, false
		}
	}
}

// lockPID is the process holding a session's writer lock, 0 when none does.
func lockPID(sessionDir string) int {
	data, err := os.ReadFile(filepath.Join(sessionDir, "writer.lock"))
	if err != nil {
		return 0
	}
	var lock struct {
		PID int `json:"pid"`
	}
	if json.Unmarshal(data, &lock) != nil {
		return 0
	}
	return lock.PID
}

// sessionOpen reports whether a live process holds the session's writer lock.
func sessionOpen(workspace, id string) bool {
	return alive(lockPID(filepath.Join(workspace, ".empryo", "sessions", id)))
}
