package src

import (
	"context"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"golang.org/x/term"

	"github.com/codeliter/codeforge/src/jobs"
)

// codeforge job watch redraws what job show prints (or job list, without an ID) once a second, until the job ends or
// the user leaves: the job's checks and the end of its log as they change. The log takes the room the terminal has.
// Off a terminal there is nothing to redraw on, so the view is printed once; job wait is what an agent uses.

const (
	watchEvery = time.Second
	// defaultColumns and defaultRows are the size of a terminal that does not say its own.
	defaultColumns = 80
	defaultRows    = 24
	// minWatchTail is the least of the log's end a watch shows, however small the terminal.
	minWatchTail = 3
	// clearScreen puts the cursor at the top and clears what is below it: a redraw without the flicker of a full clear.
	clearScreen = "\x1b[H\x1b[J"
)

// jobWatch shows the job id, or every job when id is "", as it changes.
func jobWatch(ctx context.Context, st jobs.Store, id string, w io.Writer) error {
	style := codeStyleFor(ctx, w)
	room, footer := 0, true
	f, ok := w.(*os.File)
	tty := ok && term.IsTerminal(int(f.Fd()))
	frame := func() (string, bool, error) {
		now := time.Now()
		if tty {
			// The size is read for every frame, so the view follows a terminal that is resized.
			cols, r, err := term.GetSize(int(f.Fd()))
			if err != nil || cols <= 0 || r <= 0 {
				// A terminal that does not say its size is taken for the usual one, so the view is still fitted.
				cols, r = defaultColumns, defaultRows
			}
			style.width, style.wrap = cols, true
			room, footer = watchRoom(r)
		}
		if id == "" {
			list, err := st.List()
			if err != nil {
				return "", false, err //nolint:wrapcheck // says what failed
			}
			if len(list) == 0 {
				return noJobs(style), true, nil
			}
			return strings.TrimRight(fitTable(list, now, style, room), "\n"), allFinal(list), nil
		}
		j, err := st.Load(id)
		if err != nil {
			return "", false, err //nolint:wrapcheck // says which job
		}
		// Off a terminal it is likely an agent that reads this, so what the job printed is fenced as data, as job show does.
		return fitDetail(j, now, style, st.LogPath(j.ID), room, !tty), j.State.Final(), nil
	}
	if !tty {
		view, _, err := frame()
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(w, view)
		return err //nolint:wrapcheck // a write to the output
	}
	return watchLoop(ctx, w, watchEvery, func() (string, bool, error) {
		view, done, err := frame()
		if err != nil {
			return "", false, err
		}
		line := ""
		if !done {
			// Cut to the terminal like every line above it, so the frame is as many rows as it has lines.
			line = clipDisplay(style.paint(style.pal.dim, "watching · "+time.Now().Format("15:04:05")+" · ctrl+c leaves"), style.width)
		}
		return watchFrame(view, line, footer), done, nil
	})
}

// watchRoom is how many rows of a terminal that high the view may take, and whether there are rows left for the
// footer under it: a terminal of one or two rows gives them all to the view. rows 0 is a height not known.
func watchRoom(rows int) (room int, footer bool) {
	switch {
	case rows <= 0:
		return 0, true
	case rows <= watchFooter:
		return rows, false
	}
	return rows - watchFooter, true
}

// watchFrame is what one redraw prints: the view, and under it the footer's line and the line the cursor rests on
// when the terminal has rows for them. Without them nothing follows the view, so its first line is never scrolled off.
func watchFrame(view, line string, footer bool) string {
	switch {
	case !footer:
		return view
	case line == "":
		return view + "\n"
	}
	return view + "\n" + line + "\n"
}

// noJobs says there is nothing to list, in one row of a terminal however narrow.
func noJobs(st codeStyle) string {
	const none = "codeforge job: no jobs."
	if !st.wrap {
		return none
	}
	return clipDisplay(none, st.width)
}

func allFinal(list []*jobs.Job) bool {
	for _, j := range list {
		if !j.State.Final() {
			return false
		}
	}
	return true
}

// watchFooter is the rows a watch keeps under the view: the watching line, and the line the cursor rests on.
const watchFooter = 2

// fitTable is the job table fitted to room rows: the newest jobs, and how many more there are when not all fit.
// room 0 is a size not known: every job is listed.
func fitTable(list []*jobs.Job, now time.Time, st codeStyle, room int) string {
	const header = 1
	if room <= 0 || header+len(list) <= room {
		return jobTable(list, now, st)
	}
	// A row goes to saying what is left out; on a terminal with a row for nothing else, that is all it shows.
	keep := room - header - 1
	more := func(n int) string {
		return clipDisplay(st.paint(st.pal.dim, fmt.Sprintf("… %s: codeforge job list shows them all", plural(n, "more job"))), st.width) + "\n"
	}
	if keep < 0 {
		return more(len(list))
	}
	return jobTable(list[:keep], now, st) + more(len(list)-keep)
}

// fitDetail is the job's detail fitted to room rows: no line is wider than the terminal, so each takes one row, and
// the log's end gets the rows the rest leaves. Room too small for even that keeps the status line and the end of the
// view. room 0 is a size not known: the view is as job show prints it.
func fitDetail(j *jobs.Job, now time.Time, st codeStyle, log string, room int, fenced bool) string {
	view := jobDetail(j, now, st, log, fenced)
	if room <= 0 {
		return view
	}
	if tail := max(logTail+room-(strings.Count(view, "\n")+1), minWatchTail); tail != logTail {
		view = jobDetailTail(j, now, st, log, fenced, tail)
	}
	lines := strings.Split(view, "\n")
	if len(lines) > room {
		lines = slices.Concat(lines[:1], lines[len(lines)-(room-1):])
	}
	for i, l := range lines {
		lines[i] = clipDisplay(l, st.width)
	}
	return strings.Join(lines, "\n")
}

// clipDisplay shortens a line to width cells of the terminal, marking the cut: colours are kept and take no cell, a
// wide character takes two. A width of 0 is one not known, and cuts nothing.
func clipDisplay(line string, width int) string {
	if width <= 0 || ansi.StringWidth(line) <= width {
		return line
	}
	return ansi.Truncate(line, width, "…")
}

// watchLoop draws frame's view over the last one every interval, until frame says it is done or ctx ends. Leaving
// with ctrl+c is how a watch ends, so it is no error.
func watchLoop(ctx context.Context, w io.Writer, every time.Duration, frame func() (view string, done bool, err error)) error {
	for {
		view, done, err := frame()
		if err != nil {
			return err
		}
		if _, err := fmt.Fprint(w, clearScreen+view); err != nil {
			return fmt.Errorf("drawing the watch: %w", err)
		}
		if done {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(every):
		}
	}
}
