package setupspec

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/ajnart/self-host/internal/helpers"
)

// Call is one HTTP request to the App with the checks that make it a proof.
// String values in Path, JSON, Form, Header and Equals may use {user},
// {password}, {email}, {url}, {app} and any variable an earlier Capture set.
type Call struct {
	Method string            `yaml:"method,omitempty"`
	Path   string            `yaml:"path"`
	JSON   map[string]any    `yaml:"json,omitempty"`
	Form   map[string]string `yaml:"form,omitempty"`
	Header map[string]string `yaml:"header,omitempty"`
	// Basic sends {user} and {password} as HTTP basic auth.
	Basic      bool `yaml:"basic,omitempty"`
	NoRedirect bool `yaml:"no_redirect,omitempty"`
	// Ok lists the accepted statuses; empty accepts any 2xx.
	Ok []int `yaml:"ok,omitempty"`
	// Denied lists statuses that mean "no" instead of failing. A login call
	// without Denied treats 401 and 403 as a refused login; an explicit Ok
	// match wins over Denied.
	Denied []int `yaml:"denied,omitempty"`
	// Lenient turns every status outside Ok into a plain "no".
	Lenient bool `yaml:"lenient,omitempty"`
	// Capture stores a JSON value in a variable for later calls; an absent or
	// empty value counts as a refusal.
	Capture map[string]string `yaml:"capture,omitempty"`
	Assert  []Assert          `yaml:"assert,omitempty"`
}

// Assert checks one value of a JSON response. Path is dotted keys, numbers
// index arrays, and an empty Path is the whole body.
type Assert struct {
	Path    string `yaml:"path,omitempty"`
	Equals  string `yaml:"equals,omitempty"`
	Present bool   `yaml:"present,omitempty"`
	// Some requires the array at Path to have an element that meets every
	// nested assertion.
	Some []Assert `yaml:"some,omitempty"`
}

type scope map[string]string

func (s scope) replacer(escape bool) *strings.Replacer {
	pairs := make([]string, 0, 2*len(s))
	for name, value := range s {
		if escape {
			value = url.QueryEscape(value)
		}
		pairs = append(pairs, "{"+name+"}", value)
	}
	return strings.NewReplacer(pairs...)
}

func (s scope) expand(text string) string { return s.replacer(false).Replace(text) }

func (s scope) expandPath(text string) string { return s.replacer(true).Replace(text) }

func (s scope) expandValue(value any) any {
	switch v := value.(type) {
	case string:
		return s.expand(v)
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			out[key] = s.expandValue(item)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = s.expandValue(item)
		}
		return out
	}
	return value
}

func (c Call) request(vars scope) helpers.Request {
	req := helpers.Request{Method: c.Method, Path: vars.expandPath(c.Path), NoRedirect: c.NoRedirect}
	if c.JSON != nil {
		req.JSON = vars.expandValue(c.JSON)
	}
	if c.Form != nil {
		req.Form = url.Values{}
		for key, value := range c.Form {
			req.Form.Set(key, vars.expand(value))
		}
	}
	if c.Header != nil {
		req.Header = map[string]string{}
		for key, value := range c.Header {
			req.Header[key] = vars.expand(value)
		}
	}
	if c.Basic {
		req.User, req.Password = vars["user"], vars["password"]
	}
	if req.Method == "" && (c.JSON != nil || c.Form != nil) {
		req.Method = http.MethodPost
	}
	return req
}

func (c Call) accepted(status int, denied []int) (ok bool, refused bool) {
	switch {
	case slices.Contains(c.Ok, status):
		return true, false
	case slices.Contains(denied, status):
		return false, true
	case len(c.Ok) == 0 && status >= 200 && status < 300:
		return true, false
	}
	return false, c.Lenient
}

// run reports whether the call passed: false means a clean "no" (refused,
// asserted wrong or captured nothing), an error means the App misbehaved.
func (c Call) run(ctx context.Context, h *helpers.H, vars scope, defaultDenied []int) (bool, error) {
	req := c.request(vars)
	resp, err := h.HTTP(ctx, req)
	if err != nil {
		return false, err
	}
	denied := c.Denied
	if denied == nil {
		denied = defaultDenied
	}
	ok, refused := c.accepted(resp.Status, denied)
	if refused {
		return false, nil
	}
	if !ok {
		method := req.Method
		if method == "" {
			method = http.MethodGet
		}
		return false, fmt.Errorf("%s %s returned HTTP %d", method, c.Path, resp.Status)
	}
	if len(c.Capture) == 0 && len(c.Assert) == 0 {
		return true, nil
	}
	var body any
	if err := resp.Decode(&body); err != nil {
		return false, err
	}
	for name, path := range c.Capture {
		value, found := lookup(body, path)
		if !found || empty(value) {
			return false, nil
		}
		vars[name] = fmt.Sprint(value)
	}
	return holdAll(c.Assert, body, vars), nil
}

type calls []Call

// run runs every call in order and reports whether all passed.
func (cs calls) run(ctx context.Context, h *helpers.H, vars scope, defaultDenied []int) (bool, error) {
	for _, c := range cs {
		ok, err := c.run(ctx, h, vars, defaultDenied)
		if err != nil || !ok {
			return false, err
		}
	}
	return true, nil
}

func (cs calls) validate(role string) error {
	for i, c := range cs {
		if c.Path == "" {
			return fmt.Errorf("%s call %d has no path", role, i+1)
		}
	}
	return nil
}

func holdAll(asserts []Assert, body any, vars scope) bool {
	return !slices.ContainsFunc(asserts, func(a Assert) bool { return !a.holds(body, vars) })
}

func (a Assert) holds(body any, vars scope) bool {
	value, found := lookup(body, a.Path)
	if !found {
		return false
	}
	if a.Present && empty(value) {
		return false
	}
	if a.Equals != "" && fmt.Sprint(value) != vars.expand(a.Equals) {
		return false
	}
	if len(a.Some) == 0 {
		return true
	}
	items, isList := value.([]any)
	if !isList {
		return false
	}
	return slices.ContainsFunc(items, func(item any) bool { return holdAll(a.Some, item, vars) })
}

func lookup(value any, path string) (any, bool) {
	if path == "" {
		return value, true
	}
	for _, key := range strings.Split(path, ".") {
		switch node := value.(type) {
		case map[string]any:
			next, found := node[key]
			if !found {
				return nil, false
			}
			value = next
		case []any:
			index, err := strconv.Atoi(key)
			if err != nil || index < 0 || index >= len(node) {
				return nil, false
			}
			value = node[index]
		default:
			return nil, false
		}
	}
	return value, true
}

func empty(value any) bool {
	switch v := value.(type) {
	case nil:
		return true
	case string:
		return v == ""
	case bool:
		return !v
	}
	return false
}
