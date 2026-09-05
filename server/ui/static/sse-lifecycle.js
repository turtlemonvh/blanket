/*
SSE back/forward-cache lifecycle
================================

Chrome (and Safari, and Firefox) keep a navigated-away page alive in the
back/forward cache with its `EventSource` connections still open. Every
blanket UI page opens at least one SSE stream (`/ui/sse/tasks`,
`/ui/sse/workers`, `/task/:id/log`, `/worker/:id/log`), so after a handful
of tab switches the cached pages hold enough live sockets to exhaust the
browser's six-connections-per-host HTTP/1.1 limit. The freshly loaded page's
own SSE stream and its htmx partial requests then sit `(pending)` until the
browser evicts something -- the app looks hung for ~30s.

The fix is to close the streams when the page is hidden and reopen them if
the page is restored from the cache. htmx's SSE extension (htmx-sse.js)
closes an element's `EventSource` on `htmx:beforeCleanupElement` and calls
`ensureEventSourceOnElement` on `htmx:afterProcessNode`, so firing those two
events through `htmx.trigger` is enough -- no changes to the vendored
extension.

`document.querySelectorAll` runs at event time, so elements swapped in by
htmx after page load are covered too.

Do not remove this without re-testing a Tasks -> Workers -> Tasks -> ...
navigation cycle in a real Chrome with DevTools' Network tab open.

See https://github.com/turtlemonvh/blanket/issues/103
*/

(function () {
    'use strict';

    if (typeof window === 'undefined' || typeof window.htmx === 'undefined') {
        return;
    }

    // Elements the SSE extension owns: `sse-connect` is htmx 1.9's attribute,
    // `hx-sse` its deprecated predecessor. Both are handled by the extension.
    var SSE_SELECTOR = '[sse-connect], [hx-sse], [data-sse-connect], [data-hx-sse]';

    // Tracks whether we closed the streams, so pagehide is idempotent and
    // pageshow only reopens streams this script actually closed.
    var closed = false;

    function eachStreamElement(fn) {
        var els = document.querySelectorAll(SSE_SELECTOR);
        for (var i = 0; i < els.length; i++) {
            fn(els[i]);
        }
    }

    function closeStreams() {
        if (closed) {
            return;
        }
        closed = true;
        eachStreamElement(function (el) {
            window.htmx.trigger(el, 'htmx:beforeCleanupElement');
        });
    }

    function reopenStreams() {
        if (!closed) {
            return;
        }
        closed = false;
        eachStreamElement(function (el) {
            window.htmx.trigger(el, 'htmx:afterProcessNode');
        });
    }

    // pagehide fires both when the page is frozen into the bfcache
    // (event.persisted === true) and when it is being torn down. Closing is
    // right in both cases.
    window.addEventListener('pagehide', closeStreams);

    // Only a restore from the bfcache needs a reopen; a normal load
    // (event.persisted === false) already opened its streams via htmx's own
    // htmx:afterProcessNode pass, and reopening would double the connections.
    window.addEventListener('pageshow', function (event) {
        if (event.persisted) {
            reopenStreams();
        }
    });
})();
