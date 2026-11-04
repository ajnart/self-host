package setupspec

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
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
	// Require is an Assert whose failure is an error: the response is not
	// the shape the App promises, so it is no answer at all.
	Require []Assert `yaml:"require,omitempty"`
}

// Assert checks one value of a JSON response. Path is dotted keys, numbers
// index arrays, and an empty Path is the whole body.
type Assert struct {
	Path    string `yaml:"path,omitempty"`
	Equals  string `yaml:"equals,omitempty"`
	Present bool   `yaml:"present,omitempty"`
	// Type requires a JSON bool, string, number, array or object.
	Type string `yaml:"type,omitempty"`
	// Positive requires a number above zero.
	Positive bool `yaml:"positive,omitempty"`
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
		return false, fmt.Errorf("%s %s returned HTTP %d: %s", orGet(req.Method), req.Path, resp.Status, resp.Snippet())
	}
	if len(c.Capture) == 0 && len(c.Assert) == 0 && len(c.Require) == 0 {
		return true, nil
	}
	var body any
	if err := resp.Decode(&body); err != nil {
		return false, err
	}
	if !holdAll(c.Require, body, vars) {
		return false, fmt.Errorf("%s %s returned an unexpected response", orGet(req.Method), req.Path)
	}
	for name, path := range c.Capture {
		value, found := lookup(body, path)
		if !found || empty(value) {
			return false, nil
		}
		vars[name] = fmt.Sprint(value)
		h.Log.Secret(vars[name])
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

var (
	placeholder = regexp.MustCompile(`\{([a-z_][a-z0-9_]*)\}`)
	callNames   = []string{"user", "password", "email", "url", "app"}
	textNames   = []string{"user", "email", "url", "app"}
	jsonTypes   = []string{"bool", "string", "number", "array", "object"}
	httpMethods = []string{"", http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodHead}
)

func unknownPlaceholder(text string, known []string) string {
	for _, match := range placeholder.FindAllStringSubmatch(text, -1) {
		if !slices.Contains(known, match[1]) {
			return match[1]
		}
	}
	return ""
}

func (c Call) strings() []string {
	out := []string{c.Path}
	var walk func(any)
	walk = func(v any) {
		switch v := v.(type) {
		case string:
			out = append(out, v)
		case map[string]any:
			for _, item := range v {
				walk(item)
			}
		case []any:
			for _, item := range v {
				walk(item)
			}
		}
	}
	walk(c.JSON)
	for _, v := range c.Form {
		out = append(out, v)
	}
	for _, v := range c.Header {
		out = append(out, v)
	}
	var asserts func([]Assert)
	asserts = func(list []Assert) {
		for _, a := range list {
			out = append(out, a.Equals)
			asserts(a.Some)
		}
	}
	asserts(c.Assert)
	asserts(c.Require)
	return out
}

func (a Assert) validate() error {
	if a.Type != "" && !slices.Contains(jsonTypes, a.Type) {
		return fmt.Errorf("assert type %q is not one of %s", a.Type, strings.Join(jsonTypes, ", "))
	}
	for _, nested := range a.Some {
		if err := nested.validate(); err != nil {
			return err
		}
	}
	return nil
}

func (cs calls) validate(role string) error {
	known := slices.Clone(callNames)
	for i, c := range cs {
		where := fmt.Sprintf("%s call %d", role, i+1)
		if c.Path == "" {
			return fmt.Errorf("%s has no path", where)
		}
		if !slices.Contains(httpMethods, c.Method) {
			return fmt.Errorf("%s has unknown method %q", where, c.Method)
		}
		for _, text := range c.strings() {
			if bad := unknownPlaceholder(text, known); bad != "" {
				return fmt.Errorf("%s uses unknown placeholder {%s}", where, bad)
			}
		}
		for _, list := range [][]Assert{c.Assert, c.Require} {
			for _, a := range list {
				if err := a.validate(); err != nil {
					return fmt.Errorf("%s: %w", where, err)
				}
			}
		}
		for name := range c.Capture {
			if slices.Contains(callNames, name) {
				return fmt.Errorf("%s captures %q, which is already a placeholder", where, name)
			}
			known = append(known, name)
		}
	}
	return nil
}

func orGet(method string) string {
	if method == "" {
		return http.MethodGet
	}
	return method
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
	if a.Type != "" && jsonType(value) != a.Type {
		return false
	}
	if a.Positive {
		if n, isNumber := value.(float64); !isNumber || n <= 0 {
			return false
		}
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

func jsonType(value any) string {
	switch value.(type) {
	case bool:
		return "bool"
	case string:
		return "string"
	case float64:
		return "number"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	}
	return "null"
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
