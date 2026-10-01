package httpapi

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// specOperations returns "METHOD /path" for every operation in openapi.yaml.
// The file uses a fixed layout: paths at two-space indent, methods at four.
func specOperations(t *testing.T) map[string]bool {
	t.Helper()
	ops := map[string]bool{}
	inPaths, path := false, ""
	for _, line := range strings.Split(string(openAPISpec), "\n") {
		switch {
		case line == "paths:":
			inPaths = true
		case inPaths && line != "" && !strings.HasPrefix(line, " "):
			inPaths = false
		case inPaths && strings.HasPrefix(line, "  /") && strings.HasSuffix(line, ":"):
			path = strings.TrimSuffix(strings.TrimSpace(line), ":")
		case inPaths && path != "" && strings.HasPrefix(line, "    ") && !strings.HasPrefix(line, "     "):
			m := strings.TrimSuffix(strings.TrimSpace(line), ":")
			switch m {
			case "get", "post", "put", "patch", "delete":
				ops[strings.ToUpper(m)+" "+path] = true
			}
		}
	}
	return ops
}

func TestSpecCoversEveryRoute(t *testing.T) {
	ops := specOperations(t)
	if !ops["GET /metrics"] {
		t.Error("spec is missing GET /metrics")
	}
	for _, rt := range (&handler{}).routes() {
		pattern := strings.Replace(rt.pattern, "/{$}", "/", 1)
		if pattern == "GET /docs" || pattern == "GET /openapi.yaml" {
			continue // the documentation itself
		}
		if !ops[pattern] {
			t.Errorf("route %q is not documented in openapi.yaml", rt.pattern)
		}
		delete(ops, pattern)
	}
	delete(ops, "GET /metrics")
	for op := range ops {
		t.Errorf("openapi.yaml documents %q but no such route exists", op)
	}
}

func TestDocsAndSpecAreServed(t *testing.T) {
	h := NewHandler(Deps{DB: fakePinger{}})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/openapi.yaml", nil))
	if rr.Code != 200 || !strings.HasPrefix(rr.Body.String(), "openapi: 3") {
		t.Fatalf("openapi.yaml: %d", rr.Code)
	}
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/docs", nil))
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), "SwaggerUIBundle") || !strings.Contains(rr.Body.String(), `url: "/openapi.yaml"`) {
		t.Fatalf("docs page: %d", rr.Code)
	}
}
