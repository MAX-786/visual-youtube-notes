package generation

import (
	"encoding/json"
	"strings"
	"testing"
)

// The rules OpenAI's strict structured outputs enforce, checked over the
// whole transformed schema rather than the parts a reader remembers. The
// first live call was refused on a node this walk would have caught.
func TestRequestSchemaSatisfiesStrictMode(t *testing.T) {
	var root map[string]any
	if err := json.Unmarshal(RequestSchema(), &root); err != nil {
		t.Fatal(err)
	}
	var problems []string
	var walk func(node map[string]any, path string)
	walk = func(node map[string]any, path string) {
		_, hasType := node["type"]
		_, hasRef := node["$ref"]
		_, hasAnyOf := node["anyOf"]
		if _, ok := node["const"]; ok {
			problems = append(problems, path+": const is not a strict-mode keyword")
		}
		if _, ok := node["oneOf"]; ok {
			problems = append(problems, path+": oneOf is not a strict-mode keyword")
		}
		if _, ok := node["minItems"]; ok {
			problems = append(problems, path+": minItems should have been stripped")
		}
		isSchema := hasType || hasRef || hasAnyOf
		for _, k := range []string{"enum", "properties", "items"} {
			if _, ok := node[k]; ok {
				isSchema = true
			}
		}
		if isSchema && !hasType && !hasRef && !hasAnyOf {
			problems = append(problems, path+": no type")
		}
		if enum, ok := node["enum"].([]any); ok {
			nullable := false
			if types, ok := node["type"].([]any); ok {
				for _, x := range types {
					if x == "null" {
						nullable = true
					}
				}
			}
			hasNull := false
			for _, v := range enum {
				if v == nil {
					hasNull = true
				}
			}
			if nullable != hasNull {
				problems = append(problems, path+": a nullable enum must list null, and only then")
			}
		}
		if props, ok := node["properties"].(map[string]any); ok {
			if node["additionalProperties"] != false {
				problems = append(problems, path+": additionalProperties must be false")
			}
			required := map[string]bool{}
			if list, ok := node["required"].([]any); ok {
				for _, r := range list {
					required[r.(string)] = true
				}
			}
			for name, child := range props {
				if !required[name] {
					problems = append(problems, path+"."+name+": every property must be required")
				}
				if m, ok := child.(map[string]any); ok {
					walk(m, path+".properties."+name)
				}
			}
		}
		if defs, ok := node["$defs"].(map[string]any); ok {
			for name, child := range defs {
				if m, ok := child.(map[string]any); ok {
					walk(m, path+".$defs."+name)
				}
			}
		}
		if items, ok := node["items"].(map[string]any); ok {
			walk(items, path+".items")
		}
		if list, ok := node["anyOf"].([]any); ok {
			for i, v := range list {
				if m, ok := v.(map[string]any); ok {
					walk(m, path+".anyOf["+string(rune('0'+i))+"]")
				}
			}
		}
	}
	walk(root, "$")
	if len(problems) > 0 {
		t.Fatalf("strict-mode problems:\n%s", strings.Join(problems, "\n"))
	}

	// The nodes the live provider refused, spelled the way it accepts.
	defs := root["$defs"].(map[string]any)
	mindMapType := defs["mindMap"].(map[string]any)["properties"].(map[string]any)["type"].(map[string]any)
	if mindMapType["type"] != "string" || len(mindMapType["enum"].([]any)) != 1 || mindMapType["enum"].([]any)[0] != "mind_map" {
		t.Fatalf("mindMap.type became %v", mindMapType)
	}
	language := root["properties"].(map[string]any)["detected_language"].(map[string]any)
	if language["type"] != "string" {
		t.Fatalf("detected_language became %v", language)
	}
	orientation := defs["timeline"].(map[string]any)["properties"].(map[string]any)["orientation"].(map[string]any)
	if orientation["type"] != "string" {
		t.Fatalf("orientation became %v", orientation)
	}
}
