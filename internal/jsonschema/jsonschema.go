// Package jsonschema is the subset of JSON Schema (draft 2020-12) deps generates for its
// configuration: a Schema type that marshals in a stable key order, and Reflect, which
// derives a schema from Go structs through their yaml and jsonschema tags.
package jsonschema

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"iter"
	"reflect"
	"strconv"
	"strings"
)

// Version is the JSON Schema dialect of reflected schemas.
const Version = "https://json-schema.org/draft/2020-12/schema"

// Schema is a JSON Schema node. Fields marshal in declaration order, Extras (keywords
// without a field, such as markdownDescription) last and sorted.
type Schema struct {
	Version              string         `json:"$schema,omitzero"`
	ID                   string         `json:"$id,omitzero"`
	Ref                  string         `json:"$ref,omitzero"`
	Definitions          Definitions    `json:"$defs,omitzero"`
	AnyOf                []*Schema      `json:"anyOf,omitzero"`
	AllOf                []*Schema      `json:"allOf,omitzero"`
	If                   *Schema        `json:"if,omitzero"`
	Then                 *Schema        `json:"then,omitzero"`
	Items                *Schema        `json:"items,omitzero"`
	Properties           *Properties    `json:"properties,omitzero"`
	AdditionalProperties *Schema        `json:"additionalProperties,omitzero"`
	PropertyNames        *Schema        `json:"propertyNames,omitzero"`
	Type                 string         `json:"type,omitzero"`
	Enum                 []any          `json:"enum,omitzero"`
	Minimum              jsontext.Value `json:"minimum,omitzero"`
	Pattern              string         `json:"pattern,omitzero"`
	MinItems             *uint64        `json:"minItems,omitzero"`
	Required             []string       `json:"required,omitzero"`
	Title                string         `json:"title,omitzero"`
	Description          string         `json:"description,omitzero"`
	Default              any            `json:"default,omitzero"`
	Extras               map[string]any `json:"-"`
}

// FalseSchema is the schema false, which matches nothing (additionalProperties: false).
var FalseSchema = &Schema{}

// Definitions are the named schemas under $defs.
type Definitions map[string]*Schema

// fields is Schema without its MarshalJSONTo method.
type fields Schema

// MarshalJSONTo writes FalseSchema as false and any other schema as an object with the
// Extras after the fields.
func (s *Schema) MarshalJSONTo(enc *jsontext.Encoder) error {
	if s == FalseSchema {
		return enc.WriteToken(jsontext.False)
	}
	if len(s.Extras) == 0 {
		return json.MarshalEncode(enc, (*fields)(s))
	}
	b, err := json.Marshal((*fields)(s), json.Deterministic(true))
	if err != nil {
		return err
	}
	extras, err := json.Marshal(s.Extras, json.Deterministic(true))
	if err != nil {
		return err
	}
	if len(b) > 2 { // not {}
		b = append(b[:len(b)-1], ',')
	} else {
		b = b[:1]
	}
	return enc.WriteValue(append(b, extras[1:]...))
}

// Properties are the properties of an object schema in insertion order.
type Properties struct {
	keys   []string
	values map[string]*Schema
}

// NewProperties returns empty properties.
func NewProperties() *Properties { return &Properties{values: map[string]*Schema{}} }

// Set adds or replaces property key; a new key goes last.
func (p *Properties) Set(key string, s *Schema) {
	if _, ok := p.values[key]; !ok {
		p.keys = append(p.keys, key)
	}
	p.values[key] = s
}

// Value returns the schema of property key, or nil.
func (p *Properties) Value(key string) *Schema { return p.values[key] }

// All yields the properties in insertion order.
func (p *Properties) All() iter.Seq2[string, *Schema] {
	return func(yield func(string, *Schema) bool) {
		for _, k := range p.keys {
			if !yield(k, p.values[k]) {
				return
			}
		}
	}
}

// MarshalJSONTo writes the properties as an object in insertion order.
func (p *Properties) MarshalJSONTo(enc *jsontext.Encoder) error {
	if err := enc.WriteToken(jsontext.BeginObject); err != nil {
		return err
	}
	for k, s := range p.All() {
		if err := enc.WriteToken(jsontext.String(k)); err != nil {
			return err
		}
		if err := json.MarshalEncode(enc, s); err != nil {
			return err
		}
	}
	return enc.WriteToken(jsontext.EndObject)
}

// Reflect derives the schema of the struct v points to. The root object is inlined; other
// structs become $defs entries named after their type, referenced with $ref. Objects reject
// unknown keys (additionalProperties: false).
//
// Property names come from the yaml tags (fields tagged "-" and unexported fields are
// skipped), descriptions from jsonschema_description. The jsonschema tag is a comma-separated
// list of required, enum=<v> (repeatable), default=<v>, minimum=<n>, minItems=<n> and
// pattern=<re>; on a list, enum and pattern apply to its items.
func Reflect(v any) (*Schema, error) {
	t := reflect.TypeOf(v)
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == nil || t.Kind() != reflect.Struct {
		return nil, fmt.Errorf("reflect %T: not a struct", v)
	}
	r := reflector{defs: Definitions{}}
	s, err := r.object(t)
	if err != nil {
		return nil, err
	}
	s.Version = Version
	if len(r.defs) > 0 {
		s.Definitions = r.defs
	}
	return s, nil
}

type reflector struct{ defs Definitions }

func (r *reflector) object(t reflect.Type) (*Schema, error) {
	s := &Schema{Properties: NewProperties(), AdditionalProperties: FalseSchema, Type: "object"}
	for f := range t.Fields() {
		name, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		if !f.IsExported() || name == "-" {
			continue
		}
		if name == "" {
			name = f.Name
		}
		p, err := r.schema(f.Type)
		if err != nil {
			return nil, fmt.Errorf("%s.%s: %w", t.Name(), f.Name, err)
		}
		required, err := applyTags(p, f.Type, f.Tag)
		if err != nil {
			return nil, fmt.Errorf("%s.%s: %w", t.Name(), f.Name, err)
		}
		if required {
			s.Required = append(s.Required, name)
		}
		s.Properties.Set(name, p)
	}
	return s, nil
}

func (r *reflector) schema(t reflect.Type) (*Schema, error) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Struct:
		ref := &Schema{Ref: "#/$defs/" + t.Name()}
		if _, ok := r.defs[t.Name()]; ok {
			return ref, nil
		}
		r.defs[t.Name()] = nil // reserve the name: a recursive type refers to itself
		d, err := r.object(t)
		if err != nil {
			return nil, err
		}
		r.defs[t.Name()] = d
		return ref, nil
	case reflect.Slice, reflect.Array:
		items, err := r.schema(t.Elem())
		if err != nil {
			return nil, err
		}
		return &Schema{Items: items, Type: "array"}, nil
	case reflect.Map:
		if t.Key().Kind() != reflect.String {
			return nil, fmt.Errorf("map key %s is not a string", t.Key())
		}
		values, err := r.schema(t.Elem())
		if err != nil {
			return nil, err
		}
		return &Schema{AdditionalProperties: values, Type: "object"}, nil
	}
	typ, ok := scalarType(t)
	if !ok {
		return nil, fmt.Errorf("unsupported type %s", t)
	}
	return &Schema{Type: typ}, nil
}

func scalarType(t reflect.Type) (string, bool) {
	switch t.Kind() {
	case reflect.String:
		return "string", true
	case reflect.Bool:
		return "boolean", true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "integer", true
	case reflect.Float32, reflect.Float64:
		return "number", true
	}
	return "", false
}

// applyTags sets the keywords of field schema s (of Go type t) from its tags and reports
// whether the field is required.
func applyTags(s *Schema, t reflect.Type, tag reflect.StructTag) (required bool, err error) {
	s.Description = tag.Get("jsonschema_description")
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	item, itemType := s, t // enum and pattern target
	if s.Items != nil {
		item, itemType = s.Items, t.Elem()
		for itemType.Kind() == reflect.Pointer {
			itemType = itemType.Elem()
		}
	}
	spec := tag.Get("jsonschema")
	if spec == "" {
		return false, nil
	}
	for kw := range strings.SplitSeq(spec, ",") {
		key, val, _ := strings.Cut(kw, "=")
		switch key {
		case "required":
			required = true
		case "enum":
			v, err := parseValue(itemType, val)
			if err != nil {
				return false, fmt.Errorf("enum %q: %w", val, err)
			}
			item.Enum = append(item.Enum, v)
		case "default":
			v, err := parseValue(t, val)
			if err != nil {
				return false, fmt.Errorf("default %q: %w", val, err)
			}
			s.Default = v
		case "minimum":
			if _, err := strconv.ParseFloat(val, 64); err != nil {
				return false, fmt.Errorf("minimum %q: %w", val, err)
			}
			s.Minimum = jsontext.Value(val)
		case "minItems":
			n, err := strconv.ParseUint(val, 10, 64)
			if err != nil {
				return false, fmt.Errorf("minItems %q: %w", val, err)
			}
			s.MinItems = &n
		case "pattern":
			item.Pattern = val
		default:
			return false, fmt.Errorf("unknown jsonschema tag keyword %q", key)
		}
	}
	return required, nil
}

// parseValue converts tag value v to the JSON value of a field of type t.
func parseValue(t reflect.Type, v string) (any, error) {
	switch typ, _ := scalarType(t); typ {
	case "string":
		return v, nil
	case "boolean":
		return strconv.ParseBool(v)
	case "integer":
		return strconv.Atoi(v)
	case "number":
		return strconv.ParseFloat(v, 64)
	}
	return nil, fmt.Errorf("no tag values for type %s", t)
}
