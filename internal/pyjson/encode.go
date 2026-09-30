package pyjson

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Options selects the json.dumps form. Keys are always sorted.
type Options struct {
	// Indent renders indent=2 with ", "/": " separators; otherwise the compact
	// ","/":" form.
	Indent bool
	// ASCII escapes every non-ASCII character (ensure_ascii=True).
	ASCII bool
	// RawSurrogateBytes writes U+DC80-U+DCFF lone surrogates as their original
	// byte, as Python's surrogateescape output does, and rejects any other lone
	// surrogate. Without it, lone surrogates are escaped in ASCII mode and
	// rejected otherwise.
	RawSurrogateBytes bool
}

// Encode renders a decoded value, or a value built from Go strings, integers,
// booleans, nil, []any, and map[string]any.
func Encode(value any, options Options) ([]byte, error) {
	var output bytes.Buffer
	encoder := encoder{output: &output, options: options}
	if err := encoder.value(value, 0); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

type encoder struct {
	output  *bytes.Buffer
	options Options
}

func (e encoder) indent(level int) {
	e.output.WriteString(strings.Repeat(" ", level*2))
}

func (e encoder) value(value any, depth int) error {
	switch typed := value.(type) {
	case nil:
		e.output.WriteString("null")
	case bool:
		if typed {
			e.output.WriteString("true")
		} else {
			e.output.WriteString("false")
		}
	case string:
		return e.string(typed)
	case json.Number:
		if integer, ok := Integer(typed); ok {
			normalized, _ := new(big.Int).SetString(string(integer), 10)
			e.output.WriteString(normalized.String())
			return nil
		}
		e.output.WriteString(string(typed))
	case Float:
		e.output.WriteString(FormatFloat(float64(typed)))
	case NaN:
		e.output.WriteString("NaN")
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		fmt.Fprintf(e.output, "%d", typed)
	case []any:
		return e.array(typed, depth)
	case map[string]any:
		return e.object(typed, depth)
	default:
		return fmt.Errorf("unsupported JSON value %T", value)
	}
	return nil
}

func (e encoder) array(values []any, depth int) error {
	if len(values) == 0 {
		e.output.WriteString("[]")
		return nil
	}
	e.output.WriteByte('[')
	for index, item := range values {
		if index != 0 {
			e.output.WriteByte(',')
		}
		if e.options.Indent {
			e.output.WriteByte('\n')
			e.indent(depth + 1)
		}
		if err := e.value(item, depth+1); err != nil {
			return err
		}
	}
	if e.options.Indent {
		e.output.WriteByte('\n')
		e.indent(depth)
	}
	e.output.WriteByte(']')
	return nil
}

func (e encoder) object(values map[string]any, depth int) error {
	if len(values) == 0 {
		e.output.WriteString("{}")
		return nil
	}
	// Byte order of generalized UTF-8 equals Python's code-point key order,
	// including three-byte lone surrogates.
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	e.output.WriteByte('{')
	for index, key := range keys {
		if index != 0 {
			e.output.WriteByte(',')
		}
		if e.options.Indent {
			e.output.WriteByte('\n')
			e.indent(depth + 1)
		}
		if err := e.string(key); err != nil {
			return err
		}
		if e.options.Indent {
			e.output.WriteString(": ")
		} else {
			e.output.WriteByte(':')
		}
		if err := e.value(values[key], depth+1); err != nil {
			return err
		}
	}
	if e.options.Indent {
		e.output.WriteByte('\n')
		e.indent(depth)
	}
	e.output.WriteByte('}')
	return nil
}

// FormatFloat renders a float as Python repr and json.dumps do.
func FormatFloat(value float64) string {
	switch {
	case math.IsNaN(value):
		return "NaN"
	case math.IsInf(value, 1):
		return "Infinity"
	case math.IsInf(value, -1):
		return "-Infinity"
	case value == 0:
		if math.Signbit(value) {
			return "-0.0"
		}
		return "0.0"
	}
	scientific := strconv.FormatFloat(value, 'e', -1, 64)
	exponent, err := strconv.Atoi(scientific[strings.LastIndexByte(scientific, 'e')+1:])
	if err == nil && exponent >= -4 && exponent < 16 {
		fixed := strconv.FormatFloat(value, 'f', -1, 64)
		if !strings.ContainsRune(fixed, '.') {
			fixed += ".0"
		}
		return fixed
	}
	return scientific
}

func (e encoder) string(value string) error {
	const hexadecimal = "0123456789abcdef"
	output := e.output
	output.WriteByte('"')
	for index := 0; index < len(value); {
		if unit, width, ok := LoneSurrogate(value[index:]); ok {
			switch {
			case e.options.RawSurrogateBytes && unit >= 0xdc80 && unit <= 0xdcff:
				output.WriteByte(byte(unit - 0xdc00))
			case e.options.RawSurrogateBytes:
				return fmt.Errorf("cannot encode unsupported lone surrogate \\u%04x", unit)
			case e.options.ASCII:
				writeUnicodeEscape(output, rune(unit), hexadecimal)
			default:
				return fmt.Errorf("cannot encode lone surrogate \\u%04x as UTF-8", unit)
			}
			index += width
			continue
		}
		character, width := utf8.DecodeRuneInString(value[index:])
		if character == utf8.RuneError && width == 1 {
			writeUnicodeEscape(output, utf8.RuneError, hexadecimal)
			index++
			continue
		}
		index += width
		switch character {
		case '"', '\\':
			output.WriteByte('\\')
			output.WriteRune(character)
		case '\b':
			output.WriteString(`\b`)
		case '\f':
			output.WriteString(`\f`)
		case '\n':
			output.WriteString(`\n`)
		case '\r':
			output.WriteString(`\r`)
		case '\t':
			output.WriteString(`\t`)
		default:
			if character < 0x20 || e.options.ASCII && character > 0x7e {
				writeUnicodeEscape(output, character, hexadecimal)
			} else {
				output.WriteRune(character)
			}
		}
	}
	output.WriteByte('"')
	return nil
}

// LoneSurrogate decodes a three-byte generalized UTF-8 lone surrogate at the
// start of value.
func LoneSurrogate(value string) (uint16, int, bool) {
	if len(value) < 3 || value[0] != 0xed || value[1] < 0xa0 || value[1] > 0xbf || value[2] < 0x80 || value[2] > 0xbf {
		return 0, 0, false
	}
	unit := uint16(value[0]&0x0f)<<12 | uint16(value[1]&0x3f)<<6 | uint16(value[2]&0x3f)
	if unit < 0xd800 || unit > 0xdfff {
		return 0, 0, false
	}
	return unit, 3, true
}

func writeUnicodeEscape(output *bytes.Buffer, character rune, hexadecimal string) {
	writeUnit := func(unit uint16) {
		output.WriteString(`\u`)
		output.WriteByte(hexadecimal[unit>>12&0xf])
		output.WriteByte(hexadecimal[unit>>8&0xf])
		output.WriteByte(hexadecimal[unit>>4&0xf])
		output.WriteByte(hexadecimal[unit&0xf])
	}
	if character <= 0xffff {
		writeUnit(uint16(character))
		return
	}
	character -= 0x10000
	writeUnit(uint16(0xd800 + character>>10))
	writeUnit(uint16(0xdc00 + character&0x3ff))
}
