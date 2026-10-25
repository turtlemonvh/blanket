// Package auth holds blanket's token model (turtlemonvh/blanket#101): the
// scopes and presets a token can carry, the restrictions that narrow them,
// how a token is generated and hashed, and the Principal the server
// resolves a request to. It has no HTTP or storage code of its own; the
// bolt store lives in lib/bolt/tokens.go and the enforcement in
// server/auth.go. docs/design/auth.md is the spec.
package auth

import (
	"fmt"
	"sort"
	"strings"
)

// Scope is one permission a token can carry. See docs/design/auth.md A.2
// for exactly which routes and MCP tools each one opens.
type Scope string

const (
	ScopeRead          Scope = "read"
	ScopeSubmit        Scope = "submit"
	ScopeTasksManage   Scope = "tasks:manage"
	ScopeTypesWrite    Scope = "types:write"
	ScopeWorkersManage Scope = "workers:manage"
	// ScopeWorker is for the worker endpoints only. Tokens carrying it are
	// minted by the server per worker (phase 2); a token whose only scope
	// is worker never turns auth on (see Token.EnablesAuth).
	ScopeWorker Scope = "worker"
	// ScopeAdmin is everything, plus tokens, /config/ and /ops/*.
	ScopeAdmin Scope = "admin"
)

// AllScopes lists every scope, in the order the docs present them.
var AllScopes = []Scope{
	ScopeRead, ScopeSubmit, ScopeTasksManage, ScopeTypesWrite,
	ScopeWorkersManage, ScopeWorker, ScopeAdmin,
}

// Presets are named scope sets (A.2). operator is effectively
// admin-equivalent on the host: types:write plus workers:manage plus submit
// is code execution as the blanket user.
var Presets = map[string][]Scope{
	"viewer":   {ScopeRead},
	"agent":    {ScopeRead, ScopeSubmit},
	"operator": {ScopeRead, ScopeSubmit, ScopeTasksManage, ScopeTypesWrite, ScopeWorkersManage},
	"admin":    {ScopeAdmin},
}

// PresetNames returns the preset names, sorted.
func PresetNames() []string {
	out := make([]string, 0, len(Presets))
	for k := range Presets {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func validScope(s Scope) bool {
	for _, v := range AllScopes {
		if v == s {
			return true
		}
	}
	return false
}

// ParseScopes turns a list of scope and preset names (as typed on the
// command line, comma-separated or repeated) into a sorted, de-duplicated
// scope list. Unknown names are an error rather than being dropped: a typo
// that silently granted less would be confusing, and one that granted more
// is impossible only because of this check.
func ParseScopes(names []string) ([]Scope, error) {
	set := map[Scope]bool{}
	for _, raw := range names {
		for _, n := range strings.Split(raw, ",") {
			n = strings.TrimSpace(n)
			if n == "" {
				continue
			}
			if preset, ok := Presets[n]; ok {
				for _, s := range preset {
					set[s] = true
				}
				continue
			}
			if !validScope(Scope(n)) {
				return nil, fmt.Errorf("unknown scope or preset %q; scopes are %s and presets are %s",
					n, joinScopes(AllScopes), strings.Join(PresetNames(), ", "))
			}
			set[Scope(n)] = true
		}
	}
	if len(set) == 0 {
		return nil, fmt.Errorf("a token needs at least one scope or preset")
	}
	out := make([]Scope, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

func joinScopes(ss []Scope) string {
	parts := make([]string, len(ss))
	for i, s := range ss {
		parts[i] = string(s)
	}
	return strings.Join(parts, ", ")
}

// ScopeKey is a canonical string for a scope set, used to cache one MCP
// server per distinct set.
func ScopeKey(ss []Scope) string {
	cp := append([]Scope(nil), ss...)
	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	return joinScopes(cp)
}
