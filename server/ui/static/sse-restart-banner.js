/*
Server-restart banner
=====================

Every blanket page holds at least one SSE stream open (`/ui/sse/tasks`,
`/ui/sse/workers`, `/task/:id/log`, `/worker/:id/log`). When the server
shuts down or restarts itself it now emits a `retry:` hint plus a
`server-restarting` event on each of those streams before returning --
otherwise `http.Server.Shutdown` would wait on them forever, since it never
force-closes an active connection. See server/lifecycle.go and issue #23
phase 2.

Without this script the page would just stop updating, silently, with no
indication that what's on screen is stale. So: show a small banner on
`server-restarting`, and take it down again the moment any stream
reconnects (`htmx:sseOpen`). Reconnecting itself is not our job -- the
htmx SSE extension retries on error, and sse-lifecycle.js reopens streams
on a bfcache restore.

Hooking `htmx:sseOpen` is how we get at the EventSource objects: the SSE
extension triggers that event on the `sse-connect` element with the source
in `event.detail.source`, and htmx events bubble to the document. The
extension itself only wires up listeners named by `sse-swap` / `hx-trigger`
attributes, and the banner lives in the layout rather than inside any
`sse-connect` element, so it can't use those.
*/

(function () {
    'use strict';

    if (typeof window === 'undefined') {
        return;
    }

    var RESTART_EVENT = 'server-restarting';

    function banner() {
        return document.getElementById('server-restart-banner');
    }

    function show() {
        var el = banner();
        if (el) {
            el.hidden = false;
        }
    }

    function hide() {
        var el = banner();
        if (el) {
            el.hidden = true;
        }
    }

    // A page may hold several streams; each one gets the listener once.
    // The flag lives on the EventSource, so a reconnect (which builds a
    // fresh EventSource) is hooked again on its own htmx:sseOpen.
    function hook(source) {
        if (!source || source.blanketRestartHooked) {
            return;
        }
        source.blanketRestartHooked = true;
        source.addEventListener(RESTART_EVENT, show);
    }

    document.addEventListener('htmx:sseOpen', function (event) {
        hide();
        hook(event.detail && event.detail.source);
    });
})();
