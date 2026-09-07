# Vendored: gopkg.in/tomb.v1

A verbatim copy of `gopkg.in/tomb.v1` at commit `dd632973f1e7`
(https://github.com/go-tomb/tomb/tree/v1), by Gustavo Niemeyer,
BSD-3-Clause (see `LICENSE`). The upstream module is a single file
that has not changed since 2014; vendoring it removes a dormant
external dependency without changing any code
(turtlemonvh/blanket#147, from the Sept 2026 dependency audit #131).

It keeps its original module path in `go.mod` so that the `replace`
directive in the repository's top-level `go.mod` points both blanket's
own import (`lib/tailed_file`) and `github.com/hpcloud/tail`'s
transitive import at this copy. Do not edit `tomb.go`; if a fix is ever
needed, note it here and in the commit that makes it. Two deliberate
deviations from upstream, both mechanical: the files are `gofmt`ed
(whitespace in comments only, so `make check-fmt` passes) and one
`Fatalf` format string in `tomb_test.go` lost a stray `%q` that modern
`go vet` rejects as a missing argument.

Tests: `cd lib/tomb && go test ./...` (it is a nested module, so the
top-level `go test ./...` does not descend into it).
