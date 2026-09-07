//go:build windows

package worker

// tailUsesPolling — see tail_watch_unix.go. Windows has no inotify;
// hpcloud/tail's watcher is the same either way, so the combined log is
// built the same way, just with up to a poll interval (250ms) of
// latency before a line is recorded.
const tailUsesPolling = true
