// A stand-in for the GitHub Releases API, for scripts/upgrade.sh
// (turtlemonvh/blanket#23 phase 6).
//
// `blanket upgrade` reads a Releases API and downloads assets from it. A
// test that pointed at api.github.com would fail whenever an
// unauthenticated CI runner got rate-limited, would need the network at
// all, and could never exercise a release that does not exist yet -- which
// is exactly what the test needs: two builds of the binary under test,
// published as two releases.
//
// So the binary carries a hidden `--releases-base-url` flag (and an
// `upgrade.releasesBaseURL` config key), and this serves what it expects:
//
//   GET /repos/<owner>/<repo>/releases            -> array, newest first
//   GET /repos/<owner>/<repo>/releases/tags/<tag> -> one release
//   GET /dl/<tag>/<file>                          -> the asset bytes
//
// Layout on disk: <root>/<tag>/<files...>. Every file in a tag directory
// becomes an asset of that release, SHA256SUMS included -- which is what
// makes "a release published without checksums" testable by simply not
// putting one there.
//
// Node rather than python: the toolchain image is built on the Playwright
// base, so node is guaranteed to be there.

const http = require('http');
const fs = require('fs');
const path = require('path');

const root = process.argv[2];
const port = parseInt(process.argv[3], 10);
const repo = process.argv[4] || 'acme/blanket';

function releases() {
  // Directory names are tags; sorted descending so "newest first" matches
  // what the real API returns and `Latest` picks the one the test means.
  const tags = fs.readdirSync(root, { withFileTypes: true })
    .filter((d) => d.isDirectory())
    .map((d) => d.name)
    .sort()
    .reverse();

  return tags.map((tag) => ({
    tag_name: tag,
    name: tag,
    draft: false,
    // A `-` in the tag marks a prerelease, matching semver's convention;
    // it lets the suite check that Latest skips one.
    prerelease: tag.includes('-'),
    html_url: `http://localhost:${port}/r/${tag}`,
    assets: fs.readdirSync(path.join(root, tag)).map((name) => ({
      name,
      size: fs.statSync(path.join(root, tag, name)).size,
      browser_download_url: `http://localhost:${port}/dl/${tag}/${name}`,
    })),
  }));
}

const server = http.createServer((req, res) => {
  const url = new URL(req.url, `http://localhost:${port}`);
  const p = url.pathname;

  if (p === `/repos/${repo}/releases`) {
    res.writeHead(200, { 'Content-Type': 'application/json' });
    res.end(JSON.stringify(releases()));
    return;
  }

  const tagPrefix = `/repos/${repo}/releases/tags/`;
  if (p.startsWith(tagPrefix)) {
    const tag = decodeURIComponent(p.slice(tagPrefix.length));
    const rel = releases().find((r) => r.tag_name === tag);
    if (!rel) {
      res.writeHead(404, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({ message: 'Not Found' }));
      return;
    }
    res.writeHead(200, { 'Content-Type': 'application/json' });
    res.end(JSON.stringify(rel));
    return;
  }

  if (p.startsWith('/dl/')) {
    const rest = p.slice('/dl/'.length).split('/');
    if (rest.length !== 2 || rest.some((s) => s === '' || s.includes('..'))) {
      res.writeHead(400).end('bad asset path');
      return;
    }
    const file = path.join(root, rest[0], rest[1]);
    if (!fs.existsSync(file)) {
      res.writeHead(404).end('no such asset');
      return;
    }
    res.writeHead(200, { 'Content-Type': 'application/octet-stream' });
    fs.createReadStream(file).pipe(res);
    return;
  }

  res.writeHead(404).end('not found');
});

server.listen(port, '127.0.0.1', () => {
  // scripts/upgrade.sh waits for this line before starting the CLI.
  process.stdout.write(`fake-releases: listening on ${port}\n`);
});
