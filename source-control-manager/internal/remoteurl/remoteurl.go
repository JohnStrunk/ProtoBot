// Package remoteurl parses Git remote URLs for the SCM's remote checks: it
// finds userinfo, strips it for comparison with the canonical remote, and
// derives the <host>/<owner>/<name> of a host repository.
package remoteurl

import (
	"fmt"
	"strings"
)

// Kind is the syntax of a remote URL.
type Kind int

// The syntaxes Git accepts.
const (
	KindLocal  Kind = iota // a path, or anything else without a scheme or host
	KindScheme             // scheme://[userinfo@]host[:port]/path
	KindSCP                // [userinfo@]host:path
)

// URL is a parsed remote URL.
type URL struct {
	Kind     Kind
	Scheme   string
	Userinfo string
	HasUser  bool
	Host     string
	Path     string
	raw      string
}

// Parse splits a remote URL by the rules Git uses: a URL with "://" has a
// scheme; otherwise a colon before the first slash makes the SCP-like
// form; anything else is a local path.
func Parse(raw string) URL {
	u := URL{raw: raw}
	if scheme, rest, ok := strings.Cut(raw, "://"); ok && scheme != "" && !strings.ContainsAny(scheme, "/@:") {
		u.Kind = KindScheme
		u.Scheme = strings.ToLower(scheme)
		authority, path, _ := strings.Cut(rest, "/")
		u.Path = path
		if at := strings.LastIndex(authority, "@"); at >= 0 {
			u.HasUser = true
			u.Userinfo = authority[:at]
			authority = authority[at+1:]
		}
		u.Host = authority
		return u
	}
	colon := strings.Index(raw, ":")
	slash := strings.Index(raw, "/")
	if colon > 0 && (slash < 0 || colon < slash) {
		u.Kind = KindSCP
		hostPart := raw[:colon]
		u.Path = raw[colon+1:]
		if at := strings.LastIndex(hostPart, "@"); at >= 0 {
			u.HasUser = true
			u.Userinfo = hostPart[:at]
			hostPart = hostPart[at+1:]
		}
		u.Host = hostPart
		return u
	}
	u.Kind = KindLocal
	u.Path = raw
	return u
}

// HasCredential reports userinfo other than the fixed git@ of the SCP-like
// form: any userinfo in a URL with a scheme, a user name alone included,
// and any SCP-like userinfo but "git". This is the rule of
// `ears-manager project init`.
func (u URL) HasCredential() bool {
	switch u.Kind {
	case KindScheme:
		return u.HasUser
	case KindSCP:
		return u.HasUser && u.Userinfo != "git"
	default:
		return false
	}
}

// Stripped returns the URL with any userinfo other than the fixed git@ of
// the SCP-like form removed.
func (u URL) Stripped() string {
	switch u.Kind {
	case KindScheme:
		if !u.HasUser {
			return u.raw
		}
		return u.Scheme + "://" + u.Host + "/" + u.Path
	case KindSCP:
		if !u.HasUser || u.Userinfo == "git" {
			return u.raw
		}
		return u.Host + ":" + u.Path
	default:
		return u.raw
	}
}

// Repo is a host repository.
type Repo struct {
	Host  string
	Owner string
	Name  string
}

// String returns <host>/<owner>/<name>, the form gh takes with --repo.
func (r Repo) String() string { return r.Host + "/" + r.Owner + "/" + r.Name }

// RepoOf derives the host repository of a canonical remote URL.
func RepoOf(canonical string) (Repo, error) {
	u := Parse(canonical)
	if u.Kind == KindLocal {
		return Repo{}, fmt.Errorf("the canonical remote names no host")
	}
	host := u.Host
	if u.Kind == KindScheme {
		if i := strings.LastIndex(host, ":"); i >= 0 && !strings.Contains(host[i:], "]") {
			host = host[:i]
		}
	}
	host = strings.ToLower(host)
	path := strings.Trim(u.Path, "/")
	path = strings.TrimSuffix(path, ".git")
	parts := strings.Split(path, "/")
	if host == "" || len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return Repo{}, fmt.Errorf("the canonical remote is not of the form <host>/<owner>/<name>")
	}
	return Repo{Host: host, Owner: parts[0], Name: parts[1]}, nil
}
