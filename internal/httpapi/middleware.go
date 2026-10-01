package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"

	"github.com/go-tangra/go-tangra-auth/sdk/v4/pkg/authclient"
	"github.com/go-tangra/go-tangra-hr/v4/internal/authz"
)

// OpenAPI operation extensions.
const (
	BodyLimitExtension  = "x-freya-max-body-bytes"
	PermissionExtension = "x-freya-permission"
	PublicExtension     = "x-freya-public"
	TimeoutExtension    = "x-freya-timeout-seconds"
)

// Gateway headers.
const HeaderRequestID = "X-Request-Id"

// Verifier authenticates platform tokens (implemented by authclient.Verifier).
type Verifier interface {
	Verify(ctx context.Context, token string) (authclient.Identity, error)
}

// Identity is the caller as established from the platform token.
type Identity = authclient.Identity

// Caller returns the verified identity or ErrUnauthenticated.
func Caller(r *http.Request) (Identity, error) {
	id, ok := authclient.FromContext(r.Context())
	if !ok || id.UserID == "" || id.TenantID == "" {
		return Identity{}, ErrUnauthenticated
	}
	return id, nil
}

// RequestID returns the gateway correlation id ("" when absent).
func RequestID(r *http.Request) string { return r.Header.Get(HeaderRequestID) }

// secure sets the response headers every route shares.
func secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// authenticate requires a platform token on every non-public route.
func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.protected[r.Pattern] {
			next.ServeHTTP(w, r)
			return
		}
		if s.verifier == nil {
			WriteError(w, ErrUnauthenticated.Status, ErrUnauthenticated.Reason)
			return
		}
		id, err := s.verifier.Verify(r.Context(), authclient.BearerToken(r.Header.Get("Authorization")))
		if err != nil || id.UserID == "" || id.TenantID == "" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="freya"`)
			WriteError(w, ErrUnauthenticated.Status, ErrUnauthenticated.Reason)
			return
		}
		next.ServeHTTP(w, r.WithContext(authclient.WithIdentity(r.Context(), id)))
	})
}

// authorize enforces the route's declared x-freya-permission for the verified
// caller (defence in depth behind the gateway). It runs after authenticate, so
// every protected request reaching it carries an identity.
func (s *Server) authorize(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		perm, protected := s.perms[r.Pattern]
		if !protected || !s.protected[r.Pattern] {
			next.ServeHTTP(w, r)
			return
		}
		subj, err := Subjects(r)
		if err != nil {
			WriteError(w, ErrUnauthenticated.Status, ErrUnauthenticated.Reason)
			return
		}
		if err := authz.Require(r.Context(), s.checker, subj, perm); err != nil {
			WriteError(w, ErrForbidden.Status, ErrForbidden.Reason)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Subjects derives the authz subject from the verified platform identity: the
// gateway forwards a signed-in user on every gateway-proxied route.
func Subjects(r *http.Request) (authz.Subjects, error) {
	id, err := Caller(r)
	if err != nil {
		return authz.Subjects{}, err
	}
	return authz.User(id.TenantID, id.UserID, id.Roles), nil
}

// pathParams matches the request path against a declared template.
func pathParams(template, path string) (map[string]string, bool) {
	ts, ps := strings.Split(template, "/"), strings.Split(path, "/")
	if len(ts) != len(ps) {
		return nil, false
	}
	out := map[string]string{}
	for i, t := range ts {
		if strings.HasPrefix(t, "{") && strings.HasSuffix(t, "}") {
			out[t[1:len(t)-1]] = ps[i]
			continue
		}
		if t != ps[i] {
			return nil, false
		}
	}
	return out, true
}

// validate checks the request against the OpenAPI operation of the declared
// route the mux matched (undeclared paths pass through to the 404 handler).
func (s *Server) validate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, tmpl, _ := strings.Cut(r.Pattern, " ")
		item := s.doc.Paths.Value(tmpl)
		var op *openapi3.Operation
		if item != nil {
			op = item.GetOperation(method)
		}
		params, matched := pathParams(tmpl, r.URL.EscapedPath())
		if op == nil || !matched {
			next.ServeHTTP(w, r)
			return
		}
		route := &routers.Route{Spec: s.doc, Path: tmpl, PathItem: item, Method: method, Operation: op}
		limit, binary := int64(MaxBodyBytes), false
		if v, ok := op.Extensions[BodyLimitExtension]; ok {
			if n, ok := extensionInt(v); ok && n > 0 {
				limit, binary = n, true
			}
		}
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, limit)
		}
		body := r.Body
		if binary {
			r.Body = http.NoBody
		}
		in := &openapi3filter.RequestValidationInput{Request: r, PathParams: params, Route: route,
			Options: &openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc, ExcludeRequestBody: binary}}
		err := openapi3filter.ValidateRequest(r.Context(), in)
		if binary {
			r.Body = body
		}
		if err != nil {
			var mbe *http.MaxBytesError
			var pe *openapi3filter.ParseError
			var re *openapi3filter.RequestError
			switch {
			case errors.As(err, &mbe) || strings.Contains(err.Error(), "request body too large"):
				WriteError(w, ErrBodyTooLarge.Status, ErrBodyTooLarge.Reason)
			case errors.As(err, &re) && re.Parameter != nil && re.Parameter.In == "query" && listParams[re.Parameter.Name]:
				failParam(w, re.Parameter.Name) // the list contract names the parameter, never its value
			case errors.As(err, &pe):
				WriteError(w, ErrMalformed.Status, ErrMalformed.Reason)
			default:
				WriteError(w, ErrValidation.Status, ErrValidation.Reason)
			}
			return
		}
		next.ServeHTTP(w, r)
	})
}

func extensionInt(v any) (int64, bool) {
	switch x := v.(type) {
	case float64:
		return int64(x), true
	case int:
		return int64(x), true
	case int64:
		return x, true
	case json.Number:
		n, err := x.Int64()
		return n, err == nil
	}
	return 0, false
}

func extensionBool(v any) bool {
	b, ok := v.(bool)
	return ok && b
}
