package tools

import (
	"strings"
	"testing"
)

const (
	tStart = "__MYMCP_START_123_1__"
	tDone  = "__MYMCP_DONE_123_1__"
)

// The bug this guards against: the command and the sentinel used to be sent as two
// separate lines, so for any command slower than a few hundred milliseconds the
// sentinel was typed WHILE it ran, the tty echoed it into the middle of the
// output, and the parser truncated there. Output "cut off partway through" — or
// empty, when the command was slow enough that the echo landed near the top.
//
// The markers are now emitted on their own lines by printf, and matching is
// line-anchored, so an echoed command line containing the same strings mid-line
// cannot be mistaken for a real marker.
func TestSliceRunOutput_IgnoresEchoedCommandLine(t *testing.T) {
	pane := strings.Join([]string{
		"bryan@box:~$ printf '\\n" + tStart + "\\n'; du -sh ~/ComfyUI/models; printf '\\n" + tDone + "%s\\n' \"$?\"",
		tStart,
		"146G\t/home/bryan/ComfyUI/models",
		tDone + "0",
		"bryan@box:~$ ",
	}, "\n")

	got := sliceRunOutput(pane, tStart, tDone, "0", false)

	if !strings.Contains(got, "146G") {
		t.Fatalf("real output was dropped:\n%s", got)
	}
	if strings.Contains(got, "__MYMCP_") {
		t.Errorf("marker text leaked into output:\n%s", got)
	}
	if strings.Contains(got, "du -sh") {
		t.Errorf("echoed command line leaked into output:\n%s", got)
	}
	if !strings.Contains(got, "exit code 0") {
		t.Errorf("missing exit code footer:\n%s", got)
	}
}

// Scrollback from earlier commands must not be returned as this command's output.
// Returning a fixed slab of history is what made the pane look like it was
// "echoing the entire session" and made a stale result indistinguishable from a
// fresh one.
func TestSliceRunOutput_ExcludesPriorScrollback(t *testing.T) {
	pane := strings.Join([]string{
		"bryan@box:~$ df -h",
		"/dev/nvme0n1p2  1.8T  691G  1.1T  40% /home",
		"bryan@box:~$ printf ...",
		tStart,
		"53G\t/home/bryan/models",
		tDone + "0",
	}, "\n")

	got := sliceRunOutput(pane, tStart, tDone, "0", false)

	if strings.Contains(got, "df -h") || strings.Contains(got, "1.8T") {
		t.Errorf("previous command's output leaked in:\n%s", got)
	}
	if !strings.Contains(got, "53G") {
		t.Errorf("current output missing:\n%s", got)
	}
}

// A stale marker from an earlier invocation in the same session must not win.
// Markers embed a timestamp+counter, so an older one can only precede the current
// call; taking the LAST start is what keeps the slice on this invocation.
func TestSliceRunOutput_TakesLastStart(t *testing.T) {
	pane := strings.Join([]string{
		tStart,
		"stale output from an earlier run",
		tDone + "0",
		"bryan@box:~$ printf ...",
		tStart,
		"fresh output",
		tDone + "0",
	}, "\n")

	got := sliceRunOutput(pane, tStart, tDone, "0", false)

	if strings.Contains(got, "stale") {
		t.Errorf("stale invocation leaked in:\n%s", got)
	}
	if !strings.Contains(got, "fresh output") {
		t.Errorf("current output missing:\n%s", got)
	}
}

// When output is longer than the capture window the START marker scrolls away.
// Returning a partial result is fine; returning it while looking complete is not.
func TestSliceRunOutput_FlagsTruncatedHead(t *testing.T) {
	pane := strings.Join([]string{
		"line that lost its start marker",
		"more output",
		tDone + "0",
	}, "\n")

	got := sliceRunOutput(pane, tStart, tDone, "0", false)

	if !strings.Contains(got, "truncated") {
		t.Errorf("silent truncation — caller cannot tell the head is missing:\n%s", got)
	}
}

// A command that never finishes must return what it has printed so far AND say
// clearly that it is still running, so "no output yet" is not read as "done".
func TestSliceRunOutput_TimeoutKeepsPartialOutput(t *testing.T) {
	pane := strings.Join([]string{
		tStart,
		"Loading model ...",
		"llama_server listening on 8085",
	}, "\n")

	got := sliceRunOutput(pane, tStart, tDone, "", true)

	if !strings.Contains(got, "listening on 8085") {
		t.Errorf("partial output dropped on timeout:\n%s", got)
	}
	if !strings.Contains(got, "still running") {
		t.Errorf("timeout not signalled:\n%s", got)
	}
	if strings.Contains(got, "exit code") {
		t.Errorf("reported an exit code for a command that never finished:\n%s", got)
	}
}

// Non-zero exit codes must survive to the caller; swallowing them turns a failed
// command into one that looks like it produced no output.
func TestSliceRunOutput_PropagatesNonZeroExit(t *testing.T) {
	pane := strings.Join([]string{
		tStart,
		"du: cannot access '/nope': No such file or directory",
		tDone + "1",
	}, "\n")

	got := sliceRunOutput(pane, tStart, tDone, "1", false)

	if !strings.Contains(got, "exit code 1") {
		t.Errorf("non-zero exit not reported:\n%s", got)
	}
	if !strings.Contains(got, "No such file") {
		t.Errorf("stderr text dropped:\n%s", got)
	}
}
