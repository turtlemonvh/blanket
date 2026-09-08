//go:build windows

package worker

// tailUsesPolling — see tail_watch_unix.go. Windows has no inotify, and
// blanket has always polled here; lib/follow delivers identical lines
// either way (there is a parity test for exactly that), so the combined
// log is built the same way, just with up to a poll interval (250ms) of
// latency before a line is recorded.
const tailUsesPolling = true
