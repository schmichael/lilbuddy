package github

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"
)

// Enforce read-only at the transport boundary, including requests constructed
// by the SDK. GraphQL uses POST even for reads, so parse its operation types.
type readOnlyTransport struct{ base http.RoundTripper }

func (t readOnlyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	path := strings.TrimRight(req.URL.Path, "/")
	if req.Method == http.MethodGet && path != "/graphql" {
		return t.base.RoundTrip(req)
	}
	if req.Method != http.MethodPost || path != "/graphql" || req.Body == nil {
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, fmt.Errorf("lilbuddy blocks GitHub write requests: %s %s", req.Method, req.URL.Path)
	}

	body, err := io.ReadAll(req.Body)
	_ = req.Body.Close()
	if err != nil {
		return nil, err
	}

	var payload struct{ Query string }
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("invalid GraphQL payload: %w", err)
	}
	document, err := parser.ParseQuery(&ast.Source{Input: payload.Query})
	if err != nil {
		return nil, fmt.Errorf("invalid GraphQL query: %w", err)
	}
	if len(document.Operations) == 0 {
		return nil, fmt.Errorf("GraphQL request has no query operation")
	}
	for _, operation := range document.Operations {
		if operation.Operation != ast.Query {
			return nil, fmt.Errorf("lilbuddy blocks GraphQL %s operations", operation.Operation)
		}
	}
	forward := req.Clone(req.Context())
	forward.Body = io.NopCloser(bytes.NewReader(body))
	return t.base.RoundTrip(forward)
}
