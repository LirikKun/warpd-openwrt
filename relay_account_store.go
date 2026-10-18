package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"
)

// Protect private persisted metadata against silent loss on the next update.
// HTTP patches use their separate allowlisted DTO parser, not Settings.
func decodePrivateProfileDocument(raw []byte, dst *profileDocument) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	if err := privateProfileValue(d, reflect.TypeOf(*dst), 0); err != nil {
		return fmt.Errorf("unsupported private profile metadata")
	}
	if _, err := d.Token(); err != io.EOF {
		return fmt.Errorf("invalid private profile document")
	}
	return json.Unmarshal(raw, dst)
}
func privateProfileValue(d *json.Decoder, t reflect.Type, depth int) error {
	if depth > 16 {
		return fmt.Errorf("nesting limit")
	}
	pointer := t.Kind() == reflect.Pointer
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	value, err := d.Token()
	if err != nil {
		return err
	}
	if value == nil {
		if pointer || t.Kind() == reflect.Map {
			return nil
		}
		return fmt.Errorf("null private field")
	}
	switch t.Kind() {
	case reflect.Struct, reflect.Map:
		if value != json.Delim('{') {
			return fmt.Errorf("object required")
		}
		fields := map[string]reflect.Type{}
		if t.Kind() == reflect.Struct {
			for i := 0; i < t.NumField(); i++ {
				f := t.Field(i)
				if f.PkgPath != "" {
					continue
				}
				name := strings.Split(f.Tag.Get("json"), ",")[0]
				if name == "-" {
					continue
				}
				if name == "" {
					name = f.Name
				}
				fields[name] = f.Type
			}
		}
		seen := map[string]bool{}
		for d.More() {
			token, e := d.Token()
			if e != nil {
				return e
			}
			key, ok := token.(string)
			if !ok || seen[key] {
				return fmt.Errorf("ambiguous key")
			}
			seen[key] = true
			child := fields[key]
			if t.Kind() == reflect.Map {
				child = t.Elem()
			}
			if child == nil {
				return fmt.Errorf("unsupported field")
			}
			if e = privateProfileValue(d, child, depth+1); e != nil {
				return e
			}
		}
		end, e := d.Token()
		if e != nil || end != json.Delim('}') {
			return fmt.Errorf("object end required")
		}
	case reflect.String:
		if _, ok := value.(string); !ok {
			return fmt.Errorf("string required")
		}
	case reflect.Bool:
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("bool required")
		}
	default:
		if _, ok := value.(float64); !ok {
			return fmt.Errorf("number required")
		}
	}
	return nil
}
