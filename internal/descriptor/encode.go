package descriptor

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

// encode writes a flat struct (string/bool/int fields, plus optional trailing pointer-to-struct tables)
// as TOML using double-quoted strings, matching how descriptors are usually written by hand.
// A non-empty header writes the fields under [header]; nested tables become [header.name].
func encode(b *strings.Builder, header string, v reflect.Value) {
	if header != "" {
		fmt.Fprintf(b, "[%s]\n", header)
	}
	t := v.Type()
	var tables []int
	for i := 0; i < t.NumField(); i++ {
		name, omitEmpty := tomlTag(t.Field(i))
		if name == "" {
			continue
		}
		f := v.Field(i)
		if f.Kind() == reflect.Pointer {
			tables = append(tables, i)
			continue
		}
		if omitEmpty && f.IsZero() {
			continue
		}
		fmt.Fprintf(b, "%s = %s\n", name, tomlValue(f))
	}
	for _, i := range tables {
		f := v.Field(i)
		if f.IsNil() {
			continue
		}
		name, _ := tomlTag(t.Field(i))
		if header != "" {
			name = header + "." + name
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		encode(b, name, f.Elem())
	}
}

func tomlTag(f reflect.StructField) (name string, omitEmpty bool) {
	tag := f.Tag.Get("toml")
	if tag == "-" {
		return "", false
	}
	name, opts, _ := strings.Cut(tag, ",")
	if name == "" {
		name = f.Name
	}
	return name, strings.Contains(opts, "omitempty")
}

func tomlValue(v reflect.Value) string {
	switch v.Kind() {
	case reflect.String:
		return tomlQuote(v.String())
	case reflect.Bool:
		return strconv.FormatBool(v.Bool())
	case reflect.Int, reflect.Int64, reflect.Int32:
		return strconv.FormatInt(v.Int(), 10)
	}
	panic("descriptor: unsupported field kind " + v.Kind().String())
}

// tomlQuote renders s as a TOML basic string.
func tomlQuote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\t':
			b.WriteString(`\t`)
		case '\r':
			b.WriteString(`\r`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\u%04X`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}
