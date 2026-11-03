package homarr

import (
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
)

type procedureData struct {
	JSON json.RawMessage `json:"json"`
	Meta json.RawMessage `json:"meta"`
}

type procedureUndefined struct{}

var errProcedureData = errors.New("Homarr returned invalid procedure data")

func decodeProcedureData(data procedureData) (any, error) {
	var value any
	if len(data.JSON) > 0 && json.Unmarshal(data.JSON, &value) != nil {
		return nil, errProcedureData
	}
	if len(data.Meta) == 0 {
		return value, nil
	}
	var meta struct {
		Values  any      `json:"values"`
		Version *float64 `json:"v"`
	}
	if json.Unmarshal(data.Meta, &meta) != nil {
		return nil, errProcedureData
	}
	if meta.Values == nil {
		return value, nil
	}
	if len(data.JSON) == 0 {
		return nil, errProcedureData
	}
	legacy := meta.Version == nil || *meta.Version < 1
	value, err := applyProcedureUndefined(value, meta.Values, legacy, nil)
	if err != nil {
		return nil, err
	}
	omitProcedureUndefined(value)
	return value, nil
}

func applyProcedureUndefined(value, annotations any, legacy bool, path []string) (any, error) {
	switch tree := annotations.(type) {
	case map[string]any:
		for key, child := range tree {
			segments, err := procedureDataPath(key, legacy)
			if err != nil {
				return nil, err
			}
			value, err = applyProcedureUndefined(value, child, legacy, append(slices.Clone(path), segments...))
			if err != nil {
				return nil, err
			}
		}
	case []any:
		if len(tree) < 1 || len(tree) > 2 || !validProcedureAnnotation(tree[0], len(tree) == 2) {
			return nil, errProcedureData
		}
		if kind, _ := tree[0].(string); kind == "undefined" {
			return markProcedureUndefined(value, path)
		}
		if len(tree) == 2 {
			children, ok := tree[1].(map[string]any)
			if !ok {
				return nil, errProcedureData
			}
			return applyProcedureUndefined(value, children, legacy, path)
		}
	default:
		return nil, errProcedureData
	}
	return value, nil
}

func validProcedureAnnotation(annotation any, children bool) bool {
	switch kind := annotation.(type) {
	case string:
		switch kind {
		case "undefined", "bigint", "Date", "regexp", "number", "URL":
			return !children
		case "Error", "map", "set":
			return true
		}
	case []any:
		if len(kind) != 2 || children {
			return false
		}
		name, _ := kind[0].(string)
		if name != "typed-array" {
			return false
		}
		constructor, _ := kind[1].(string)
		switch constructor {
		case "Int8Array", "Uint8Array", "Int16Array", "Uint16Array", "Int32Array", "Uint32Array", "Float32Array", "Float64Array", "Uint8ClampedArray":
			return true
		}
	}
	return false
}

func procedureDataPath(text string, legacy bool) ([]string, error) {
	var segment strings.Builder
	var path []string
	for index := 0; index < len(text); index++ {
		char := text[index]
		if char == '\\' && index+1 < len(text) {
			next := text[index+1]
			if next == '.' || !legacy && next == '\\' {
				segment.WriteByte(next)
				index++
				continue
			}
		}
		if char == '\\' && !legacy {
			return nil, errProcedureData
		}
		if char == '.' {
			path = append(path, segment.String())
			segment.Reset()
			continue
		}
		segment.WriteByte(char)
	}
	return append(path, segment.String()), nil
}

func markProcedureUndefined(value any, path []string) (any, error) {
	if len(path) == 0 {
		if value != nil {
			return nil, errProcedureData
		}
		return procedureUndefined{}, nil
	}
	key := path[0]
	if key == "__proto__" || key == "prototype" || key == "constructor" {
		return nil, errProcedureData
	}
	switch parent := value.(type) {
	case map[string]any:
		child, present := parent[key]
		if !present {
			return nil, errProcedureData
		}
		marked, err := markProcedureUndefined(child, path[1:])
		if err != nil {
			return nil, err
		}
		parent[key] = marked
	case []any:
		index, err := strconv.Atoi(key)
		if err != nil || index < 0 || index >= len(parent) || strconv.Itoa(index) != key {
			return nil, errProcedureData
		}
		marked, err := markProcedureUndefined(parent[index], path[1:])
		if err != nil {
			return nil, err
		}
		parent[index] = marked
	default:
		return nil, errProcedureData
	}
	return value, nil
}

func omitProcedureUndefined(value any) {
	switch container := value.(type) {
	case map[string]any:
		for key, child := range container {
			if _, undefined := child.(procedureUndefined); undefined {
				delete(container, key)
				continue
			}
			omitProcedureUndefined(child)
		}
	case []any:
		for _, child := range container {
			omitProcedureUndefined(child)
		}
	}
}
