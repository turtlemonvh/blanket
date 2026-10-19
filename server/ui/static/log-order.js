// Log line order for the task and worker log panes (turtlemonvh/blanket#216).
//
// A phone shows a few lines of a log at a time, so there the useful end is
// the newest one -- at the top, where the page opens. The "Newest first"
// toggle reverses the pane and, for a live pane, makes each streamed line
// land at the top (hx-swap="afterbegin") instead of the bottom. It still
// tails; only the direction changes.
//
// Defaults: newest-first on a narrow screen, oldest-first on desktop --
// which, with "Pin to bottom", is exactly how the panes behaved before.
// An explicit choice is remembered for the tab (sessionStorage) so it
// survives the stdout/stderr/both toggle, which re-renders the pane; it is
// not stored permanently, so a desktop choice never leaks onto a phone.
//
// Every pane's markup puts one log line per "\n"-terminated line of its
// HTML (a stored pane, a streamed raw line, and a streamed badged <span>
// alike), so reversing is a split on "\n" -- no per-line wrapper needed.
(function () {
  var KEY = 'blanket.logNewestFirst';
  var NARROW = '(max-width: 640px)';

  function stored() {
    try {
      var v = window.sessionStorage.getItem(KEY);
      return v === null ? null : v === '1';
    } catch (e) {
      return null;
    }
  }

  function remember(v) {
    try {
      window.sessionStorage.setItem(KEY, v ? '1' : '0');
    } catch (e) {
      /* private mode etc.: the default applies next time */
    }
  }

  function defaultNewestFirst() {
    var s = stored();
    if (s !== null) return s;
    return !!(window.matchMedia && window.matchMedia(NARROW).matches);
  }

  function reverseLines(pane) {
    var html = pane.innerHTML;
    if (!html) return;
    var lines = html.split('\n');
    var trailing = lines.length > 1 && lines[lines.length - 1] === '';
    if (trailing) lines.pop();
    lines.reverse();
    pane.innerHTML = lines.join('\n') + (trailing ? '\n' : '');
  }

  // attach wires one pane. opts:
  //   pane      the <pre> holding the log
  //   order     the "Newest first" checkbox
  //   pin       the pin checkbox (optional)
  //   pinLabel  element whose text names the pin's direction (optional)
  function attach(opts) {
    var pane = opts.pane;
    var order = opts.order;
    var pin = opts.pin;
    if (!pane || !order) return;
    var live = pane.hasAttribute('sse-swap');
    var newest = false; // the server always renders oldest-first

    function follow() {
      if (pin && !pin.checked) return;
      pane.scrollTop = newest ? 0 : pane.scrollHeight;
    }

    function set(v) {
      if (v !== newest) reverseLines(pane);
      newest = v;
      order.checked = v;
      pane.setAttribute('data-newest-first', v ? 'true' : 'false');
      if (live) pane.setAttribute('hx-swap', v ? 'afterbegin' : 'beforeend');
      if (opts.pinLabel) opts.pinLabel.textContent = v ? 'Pin to top' : 'Pin to bottom';
      follow();
    }

    order.addEventListener('change', function () {
      remember(order.checked);
      set(order.checked);
    });
    if (pin) pin.addEventListener('change', follow);
    pane.addEventListener('htmx:sseMessage', follow);
    // A pane filled by its own hx-get (a stopped worker's log tail) arrives
    // oldest-first after this ran; put it in the chosen order.
    pane.addEventListener('htmx:afterSwap', function (evt) {
      if (live || evt.target !== pane) return;
      if (newest) reverseLines(pane);
      follow();
    });

    set(defaultNewestFirst());
  }

  window.blanketLogOrder = { attach: attach };
})();
