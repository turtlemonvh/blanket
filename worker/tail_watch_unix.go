//go:build !windows

package worker

// tailUsesPolling decides how the worker's combined-log tailers notice
// that a task's log file has grown.
//
// On unix, fsnotify (inotify): the notification is immediate, which keeps the
// recorded order of two lines written a few milliseconds apart on
// different streams the order the task actually wrote them in. Polling
// would round both to the same 250ms bucket and record whichever stream
// the poller happened to reach first.
//
// (lib/tailed_file polls instead, and for a different reason: it opens a
// tailer per *viewer* of a log pane, on files that may not be local. This
// is one tailer per running task, on files this same process just
// created.)
const tailUsesPolling = false
