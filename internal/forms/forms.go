// Package forms validates the restricted, flat ACP elicitation form schema.
package forms

import (
	"encoding/json"
	"fmt"
	"math"
	"net/mail"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/BrokkAi/acp-go/schema"
)

type Option struct{ Value, Label string }
type Field struct {
	Name, Title, Description, Kind, Default string
	Required                                bool
	Options                                 []Option
	Property                                schema.ElicitationPropertySchema
}

func Fields(s schema.ElicitationSchema) ([]Field, error) {
	names := make([]string, 0, len(s.Properties))
	for name := range s.Properties {
		names = append(names, name)
	}
	sort.Strings(names)
	var fields []Field
	for _, name := range names {
		p := s.Properties[name]
		b, _ := json.Marshal(p)
		var raw struct {
			Type, Title, Description string
			Default                  json.RawMessage
			Enum                     []string
			OneOf                    []schema.EnumOption
			Items                    struct {
				Enum  []string
				AnyOf []schema.EnumOption
				OneOf []schema.EnumOption
			}
		}
		if err := json.Unmarshal(b, &raw); err != nil {
			return nil, err
		}
		f := Field{Name: name, Title: raw.Title, Description: raw.Description, Kind: raw.Type, Property: p}
		if f.Title == "" {
			f.Title = name
		}
		for _, required := range s.Required {
			if required == name {
				f.Required = true
			}
		}
		for _, secret := range []string{"password", "api_key", "apikey", "access_token", "refresh_token", "private_key", "recovery_code", "credit_card"} {
			if strings.ReplaceAll(strings.ToLower(name), "-", "_") == secret {
				return nil, fmt.Errorf("%s must be requested through URL authentication, not a form", f.Title)
			}
		}
		switch f.Kind {
		case "string", "number", "integer", "boolean", "array":
		default:
			return nil, fmt.Errorf("unsupported field type %q", f.Kind)
		}
		if f.Kind == "string" && len(raw.Default) > 0 {
			_ = json.Unmarshal(raw.Default, &f.Default)
		} else if string(raw.Default) != "null" {
			f.Default = string(raw.Default)
		}
		for _, value := range raw.Enum {
			f.Options = append(f.Options, Option{value, value})
		}
		for _, o := range raw.OneOf {
			f.Options = append(f.Options, Option{o.Const, o.Title})
		}
		if f.Kind == "boolean" {
			f.Options = []Option{{"false", "No"}, {"true", "Yes"}}
			if f.Default == "" {
				f.Default = "false"
			}
		}
		if f.Kind == "array" {
			for _, v := range raw.Items.Enum {
				f.Options = append(f.Options, Option{v, v})
			}
			for _, v := range append(raw.Items.AnyOf, raw.Items.OneOf...) {
				f.Options = append(f.Options, Option{v.Const, v.Title})
			}
		}
		fields = append(fields, f)
	}
	return fields, nil
}

func (f Field) Parse(raw string) (any, error) {
	invalid := func(why string) (any, error) { return nil, fmt.Errorf("%s: %s", f.Title, why) }
	if raw == "" {
		if !f.Required {
			return nil, nil
		}
		if f.Kind != "string" {
			return invalid("a value is required")
		}
	}
	allowed := func(v string) bool {
		if len(f.Options) == 0 {
			return true
		}
		for _, o := range f.Options {
			if o.Value == v {
				return true
			}
		}
		return false
	}
	switch f.Kind {
	case "string":
		p := f.Property.String
		if !allowed(raw) {
			return invalid("choose an offered value")
		}
		length := uint32(utf8.RuneCountInString(raw))
		if p.MinLength != nil && length < *p.MinLength {
			return invalid(fmt.Sprintf("minimum length is %d", *p.MinLength))
		}
		if p.MaxLength != nil && length > *p.MaxLength {
			return invalid(fmt.Sprintf("maximum length is %d", *p.MaxLength))
		}
		if p.Pattern != nil {
			pattern, err := regexp.Compile(*p.Pattern)
			if err != nil {
				return invalid("unsupported validation pattern")
			}
			if !pattern.MatchString(raw) {
				return invalid("does not match " + *p.Pattern)
			}
		}
		if p.Format != nil {
			switch string(*p.Format) {
			case "email":
				a, err := mail.ParseAddress(raw)
				if err != nil || a.Address != raw {
					return invalid("enter an email address")
				}
			case "uri":
				u, err := url.Parse(raw)
				if err != nil || u.Scheme == "" {
					return invalid("enter an absolute URI")
				}
			case "date":
				if _, err := time.Parse("2006-01-02", raw); err != nil {
					return invalid("use YYYY-MM-DD")
				}
			case "date-time":
				if _, err := time.Parse(time.RFC3339, raw); err != nil {
					return invalid("use an RFC3339 date and time")
				}
			}
		}
		return raw, nil
	case "integer":
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return invalid("enter an integer")
		}
		p := f.Property.Integer
		if p.Minimum != nil && value < *p.Minimum {
			return invalid(fmt.Sprintf("minimum is %d", *p.Minimum))
		}
		if p.Maximum != nil && value > *p.Maximum {
			return invalid(fmt.Sprintf("maximum is %d", *p.Maximum))
		}
		return value, nil
	case "number":
		value, err := strconv.ParseFloat(raw, 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
			return invalid("enter a finite number")
		}
		p := f.Property.Number
		if p.Minimum != nil && value < *p.Minimum {
			return invalid(fmt.Sprintf("minimum is %g", *p.Minimum))
		}
		if p.Maximum != nil && value > *p.Maximum {
			return invalid(fmt.Sprintf("maximum is %g", *p.Maximum))
		}
		return value, nil
	case "boolean":
		value, err := strconv.ParseBool(raw)
		if err != nil {
			return invalid("choose Yes or No")
		}
		return value, nil
	case "array":
		var values []string
		if err := json.Unmarshal([]byte(raw), &values); err != nil {
			return invalid("select values from the list")
		}
		p := f.Property.Array
		if p.MinItems != nil && uint64(len(values)) < *p.MinItems {
			return invalid(fmt.Sprintf("select at least %d", *p.MinItems))
		}
		if p.MaxItems != nil && uint64(len(values)) > *p.MaxItems {
			return invalid(fmt.Sprintf("select at most %d", *p.MaxItems))
		}
		seen := map[string]bool{}
		for _, v := range values {
			if !allowed(v) || seen[v] {
				return invalid("invalid or duplicate selection")
			}
			seen[v] = true
		}
		return values, nil
	}
	return invalid("unsupported field type")
}

func Values(fields []Field, values map[string]string) (map[string]schema.ElicitationContentValue, error) {
	result := map[string]schema.ElicitationContentValue{}
	for _, field := range fields {
		value, err := field.Parse(values[field.Name])
		if err != nil {
			return nil, err
		}
		if value != nil {
			result[field.Name] = value
		}
	}
	return result, nil
}
