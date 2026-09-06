// Package contract embeds the OpenAPI document and exposes what the API derives
// from it at boot (ADR 0015, ADR 0016): the route table used for request
// validation, the rate-limit classes, and the operations the extension may
// call. Nothing about the wire surface is declared a second time in Go.
package contract

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/gorillamux"
)

//go:generate go run copy_spec.go

//go:embed openapi.yaml
var specYAML []byte

// Class is one rate-limit budget from x-rate-limit-classes. A class declares a
// token bucket, a concurrency cap, or both; every bucketed class also has a
// global bucket, which is the guarantee (ADR 0016).
type Class struct {
	Name                string  `json:"-"`
	Key                 string  `json:"key"`
	Burst               int     `json:"burst"`
	RefillSeconds       float64 `json:"refill_seconds"`
	GlobalBurst         int     `json:"global_burst"`
	GlobalRefillSeconds float64 `json:"global_refill_seconds"`
	Concurrency         int     `json:"concurrency"`
	GlobalConcurrency   int     `json:"global_concurrency"`
	Mode                string  `json:"mode"`
	Note                string  `json:"note"`
}

// HasBucket reports whether the class rate-limits by token bucket.
func (c Class) HasBucket() bool {
	return c.Burst > 0 && c.RefillSeconds > 0
}

// Operation is what the API knows about one operation beyond its handler.
type Operation struct {
	ID                string
	Method            string
	Path              string
	RateLimitClass    string
	ExtensionCallable bool
	// Public is true when the operation declares an empty security list.
	Public bool
	// AcceptsCookie and AcceptsBearer say which transports the operation's
	// security requirement admits.
	AcceptsCookie bool
	AcceptsBearer bool
}

// Contract is the loaded document and its derived tables.
type Contract struct {
	Doc        *openapi3.T
	Classes    map[string]Class
	Operations map[string]*Operation
	router     routers.Router
}

// Load parses the embedded document and derives the tables.
func Load() (*Contract, error) {
	loader := openapi3.NewLoader()
	loader.IsExternalRefsAllowed = false
	doc, err := loader.LoadFromData(specYAML)
	if err != nil {
		return nil, fmt.Errorf("parse contract: %w", err)
	}

	classes, err := parseClasses(doc)
	if err != nil {
		return nil, err
	}

	ops := map[string]*Operation{}
	for path, item := range doc.Paths.Map() {
		for method, op := range item.Operations() {
			if op.OperationID == "" {
				return nil, fmt.Errorf("%s %s has no operationId", method, path)
			}
			o := &Operation{ID: op.OperationID, Method: method, Path: path}
			if raw, ok := op.Extensions["x-rate-limit"]; ok {
				if err := decodeExtension(raw, &o.RateLimitClass); err != nil {
					return nil, fmt.Errorf("%s: x-rate-limit: %w", op.OperationID, err)
				}
			}
			if _, ok := classes[o.RateLimitClass]; !ok {
				return nil, fmt.Errorf("%s names undeclared rate-limit class %q", op.OperationID, o.RateLimitClass)
			}
			if raw, ok := op.Extensions["x-extension-callable"]; ok {
				if err := decodeExtension(raw, &o.ExtensionCallable); err != nil {
					return nil, fmt.Errorf("%s: x-extension-callable: %w", op.OperationID, err)
				}
			}
			security := doc.Security
			if op.Security != nil {
				security = *op.Security
			}
			o.Public = len(security) == 0
			for _, requirement := range security {
				for scheme := range requirement {
					switch scheme {
					case "cookieSession":
						o.AcceptsCookie = true
					case "extensionBearer":
						o.AcceptsBearer = true
					}
				}
			}
			ops[op.OperationID] = o
		}
	}

	// The document's server is the production origin. The router only needs
	// the mount prefix, so the request's host never has to match.
	doc.Servers = openapi3.Servers{&openapi3.Server{URL: "/api"}}
	router, err := gorillamux.NewRouter(doc)
	if err != nil {
		return nil, fmt.Errorf("build contract router: %w", err)
	}

	return &Contract{Doc: doc, Classes: classes, Operations: ops, router: router}, nil
}

// Resolve finds the operation a request addresses. ErrNotFound means no path
// matches; ErrMethodNotAllowed means the path exists without that method.
func (c *Contract) Resolve(r *http.Request) (*routers.Route, map[string]string, error) {
	route, params, err := c.router.FindRoute(r)
	if err != nil {
		return nil, nil, err
	}
	return route, params, nil
}

// OperationFor returns the derived operation for a resolved route.
func (c *Contract) OperationFor(route *routers.Route) *Operation {
	if route == nil || route.Operation == nil {
		return nil
	}
	return c.Operations[route.Operation.OperationID]
}

// ClassNames lists the classes in a stable order.
func (c *Contract) ClassNames() []string {
	names := make([]string, 0, len(c.Classes))
	for name := range c.Classes {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// IsNotFound reports whether a Resolve error means no path matched.
func IsNotFound(err error) bool {
	return errors.Is(err, routers.ErrPathNotFound)
}

// IsMethodNotAllowed reports whether a Resolve error means the method is wrong.
func IsMethodNotAllowed(err error) bool {
	return errors.Is(err, routers.ErrMethodNotAllowed)
}

func parseClasses(doc *openapi3.T) (map[string]Class, error) {
	raw, ok := doc.Extensions["x-rate-limit-classes"]
	if !ok {
		return nil, errors.New("contract declares no x-rate-limit-classes")
	}
	var classes map[string]Class
	if err := decodeExtension(raw, &classes); err != nil {
		return nil, fmt.Errorf("x-rate-limit-classes: %w", err)
	}
	for name, class := range classes {
		class.Name = name
		if class.Key != "ip" && class.Key != "session" {
			return nil, fmt.Errorf("class %s keys by %q, expected ip or session", name, class.Key)
		}
		if !class.HasBucket() && class.Concurrency == 0 {
			return nil, fmt.Errorf("class %s declares neither a bucket nor a concurrency cap", name)
		}
		classes[name] = class
	}
	return classes, nil
}

// decodeExtension converts whatever the loader kept for an x- extension into
// the wanted shape. kin-openapi hands back decoded values for YAML documents
// and raw JSON for others; a round trip through JSON accepts both.
func decodeExtension(raw any, into any) error {
	var data []byte
	switch v := raw.(type) {
	case json.RawMessage:
		data = v
	case []byte:
		data = v
	default:
		encoded, err := json.Marshal(v)
		if err != nil {
			return err
		}
		data = encoded
	}
	return json.Unmarshal(data, into)
}
