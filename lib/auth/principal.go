package auth

// Principal is who a request is acting as, once the server has resolved
// its credentials. With auth off every request resolves to the auth-off
// principal, which may do anything.
type Principal struct {
	// Token is the token the request presented; nil for the auth-off
	// principal.
	Token  *Token
	scopes map[Scope]bool
}

// AuthOffPrincipal is the principal every request gets while auth is off.
func AuthOffPrincipal() *Principal {
	return &Principal{}
}

// NewPrincipal wraps a token that has already been looked up and checked
// for expiry.
func NewPrincipal(t Token) *Principal {
	p := &Principal{Token: &t, scopes: map[Scope]bool{}}
	for _, s := range t.Scopes {
		p.scopes[s] = true
	}
	return p
}

// AuthOff reports whether this is the auth-off principal.
func (p *Principal) AuthOff() bool {
	return p == nil || p.Token == nil
}

// Id is the token id, or "" for the auth-off principal. It is what a task
// records as createdBy.
func (p *Principal) Id() string {
	if p.AuthOff() {
		return ""
	}
	return p.Token.Id.Hex()
}

// Scopes returns the token's scopes (nil when auth is off).
func (p *Principal) Scopes() []Scope {
	if p.AuthOff() {
		return nil
	}
	return p.Token.Scopes
}

// Has reports whether the principal holds s. admin holds everything, and
// so does the auth-off principal.
func (p *Principal) Has(s Scope) bool {
	if p.AuthOff() {
		return true
	}
	return p.scopes[ScopeAdmin] || p.scopes[s]
}

// Restricted reports whether the token carries type or task restrictions.
func (p *Principal) Restricted() bool {
	return !p.AuthOff() && p.Token.Restricted()
}

// CanSubmitType reports whether the principal may submit a task of type
// typ: it needs submit, and typ must be in its types list if it has one.
func (p *Principal) CanSubmitType(typ string) bool {
	if !p.Has(ScopeSubmit) {
		return false
	}
	if p.AuthOff() || len(p.Token.Types) == 0 {
		return true
	}
	return contains(p.Token.Types, typ)
}

// TaskRef is the part of a task the access checks look at. A struct rather
// than tasks.Task so this package stays free of the tasks package.
type TaskRef struct {
	Id        string
	Type      string
	CreatedBy string
}

// Owns reports whether the principal created the task.
func (p *Principal) Owns(t TaskRef) bool {
	return !p.AuthOff() && t.CreatedBy != "" && t.CreatedBy == p.Id()
}

// inScope reports whether a task passes the token's restrictions. Both
// lists narrow, so a token carrying both needs the task to pass both.
func (p *Principal) inScope(t TaskRef) bool {
	if p.AuthOff() {
		return true
	}
	if len(p.Token.Types) > 0 && !contains(p.Token.Types, t.Type) {
		return false
	}
	if len(p.Token.Tasks) > 0 && !contains(p.Token.Tasks, t.Id) {
		return false
	}
	return true
}

// CanReadTask: read plus the restrictions, or the principal created it. A
// token can always read the tasks it created, even ones its tasks list
// doesn't name and even without read (A.2).
func (p *Principal) CanReadTask(t TaskRef) bool {
	return p.Owns(t) || (p.Has(ScopeRead) && p.inScope(t))
}

// CanManageTask: tasks:manage, narrowed by the restrictions. Covers delete,
// pause, resume and reschedule.
func (p *Principal) CanManageTask(t TaskRef) bool {
	return p.Has(ScopeTasksManage) && p.inScope(t)
}

// CanCancelTask: tasks:manage as above, or the principal created it, so an
// agent can stop its own mistake without being able to stop anyone else's.
func (p *Principal) CanCancelTask(t TaskRef) bool {
	return p.Owns(t) || p.CanManageTask(t)
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
