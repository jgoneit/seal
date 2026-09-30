// Package pyjson reproduces the frozen Python Reference's json.loads and
// json.dumps outcomes that Seal's stored documents depend on: NaN and Infinity
// constants, lone-surrogate escapes, CPython's 4300-digit integer limit,
// Python number equality, and sorted Python-style rendering.
package pyjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

// IntegerDigitLimit is CPython's default integer-string conversion limit.
const IntegerDigitLimit = 4300

// Float is a decoded JSON float, including an overflowed ±Inf.
type Float float64

// NaN is the NaN constant. Python json.loads returns one shared NaN object,
// so NaN values compare equal inside containers.
type NaN struct{}

// ErrInvalidUTF8 reports raw input that is not valid UTF-8.
var ErrInvalidUTF8 = errors.New("invalid UTF-8")

var (
	errMultipleValues = errors.New("multiple JSON values")
	errConstantKey    = errors.New("Python JSON constant cannot be an object key")
)

// IntegerLimitError reports an integer token longer than IntegerDigitLimit.
// Syntax holds a later syntax error that the integer failure preceded, as
// CPython raises the conversion error before reaching it.
type IntegerLimitError struct {
	Syntax error
}

func (e *IntegerLimitError) Error() string {
	return fmt.Sprintf("JSON integer token exceeds the frozen Python runtime limit of %d digits.", IntegerDigitLimit)
}

// IsDepthLimit reports the standard decoder's nesting-depth failure.
func IsDepthLimit(err error) bool {
	var syntaxError *json.SyntaxError
	return errors.As(err, &syntaxError) && strings.Contains(syntaxError.Error(), "exceeded max depth")
}

// Decode parses exactly one JSON value. Objects are map[string]any, integers
// are json.Number, floats are Float, and NaN is NaN. Lone-surrogate escapes
// are kept as their three-byte generalized UTF-8 encoding.
func Decode(contents []byte) (any, error) {
	if !utf8.Valid(contents) {
		return nil, ErrInvalidUTF8
	}
	protected := protect(contents)
	decoder := json.NewDecoder(bytes.NewReader(protected))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, firstFailure(protected[:syntaxScanLimit(protected, err)], err)
	}
	firstValueEnd := int(decoder.InputOffset())
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			err = errMultipleValues
		}
		return nil, firstFailure(protected[:firstValueEnd], err)
	}
	if err := firstFailure(protected, nil); err != nil {
		return nil, err
	}
	return restore(value)
}

// firstFailure returns what CPython reports first for scanned, a protected
// prefix that ends where err (nil for none) occurs. A constant object key is a
// syntax error at its own offset; an oversized integer before the first syntax
// error wins and keeps that later error in Syntax. Decoded maps are unordered,
// so this is decided from the bytes, never while restoring.
func firstFailure(scanned []byte, err error) error {
	if keyOffset := constantKeyOffset(scanned); keyOffset >= 0 {
		scanned, err = scanned[:keyOffset], errConstantKey
	}
	if containsOversizedInteger(scanned) {
		return &IntegerLimitError{Syntax: err}
	}
	return err
}

// constantKeyOffset returns the offset of the first constant marker used as an
// object key in protected, or -1.
func constantKeyOffset(protected []byte) int {
	const marker = `"\u0000f`
	for index := 0; index < len(protected); index++ {
		if protected[index] != '"' {
			continue
		}
		start := index
		for index++; index < len(protected) && protected[index] != '"'; index++ {
			if protected[index] == '\\' {
				index++
			}
		}
		if !bytes.HasPrefix(protected[start:], []byte(marker)) {
			continue
		}
		next := index + 1
		for next < len(protected) && strings.IndexByte(" \t\r\n", protected[next]) >= 0 {
			next++
		}
		if next < len(protected) && protected[next] == ':' {
			return start
		}
	}
	return -1
}

func syntaxScanLimit(contents []byte, err error) int {
	limit := len(contents)
	var syntaxError *json.SyntaxError
	if errors.As(err, &syntaxError) && syntaxError.Offset >= 0 && syntaxError.Offset < int64(limit) {
		limit = int(syntaxError.Offset)
	}
	return limit
}

func containsOversizedInteger(contents []byte) bool {
	inString := false
	for index := 0; index < len(contents); {
		character := contents[index]
		if inString {
			index++
			if character == '\\' && index < len(contents) {
				index++
				continue
			}
			if character == '"' {
				inString = false
			}
			continue
		}
		if character == '"' {
			inString = true
			index++
			continue
		}

		numberStart := index
		if character == '-' {
			index++
			if index >= len(contents) || contents[index] < '0' || contents[index] > '9' {
				continue
			}
		} else if character < '0' || character > '9' {
			index++
			continue
		}

		digitsStart := index
		for index < len(contents) && contents[index] >= '0' && contents[index] <= '9' {
			index++
		}
		digitCount := index - digitsStart
		isInteger := true
		if index+1 < len(contents) && contents[index] == '.' &&
			contents[index+1] >= '0' && contents[index+1] <= '9' {
			isInteger = false
			index++
			for index < len(contents) && contents[index] >= '0' && contents[index] <= '9' {
				index++
			}
		}
		exponentStart := index
		if index < len(contents) && (contents[index] == 'e' || contents[index] == 'E') {
			index++
			if index < len(contents) && (contents[index] == '+' || contents[index] == '-') {
				index++
			}
			if index >= len(contents) || contents[index] < '0' || contents[index] > '9' {
				index = exponentStart
			} else {
				isInteger = false
				for index < len(contents) && contents[index] >= '0' && contents[index] <= '9' {
					index++
				}
			}
		}
		if isInteger && digitCount > IntegerDigitLimit {
			return true
		}
		if index == numberStart {
			index++
		}
	}
	return false
}

// protect rewrites what encoding/json cannot represent into marked strings:
// a bare Python constant becomes "\u0000f<token>", a lone surrogate escape
// becomes "\u0000s<hex4>", and a literal U+0000 escape is doubled.
func protect(contents []byte) []byte {
	var output bytes.Buffer
	insideString := false
	for index := 0; index < len(contents); {
		current := contents[index]
		if !insideString {
			if token, width, ok := constantAt(contents[index:]); ok {
				output.WriteString(`"\u0000f` + token + `"`)
				index += width
				continue
			}
			output.WriteByte(current)
			index++
			if current == '"' {
				insideString = true
			}
			continue
		}
		if current == '"' {
			output.WriteByte(current)
			index++
			insideString = false
			continue
		}
		if current != '\\' {
			output.WriteByte(current)
			index++
			continue
		}
		if index+1 >= len(contents) {
			output.WriteByte(current)
			index++
			continue
		}
		if contents[index+1] != 'u' || index+6 > len(contents) {
			output.Write(contents[index : index+2])
			index += 2
			continue
		}
		unit, ok := parseHexUnit(contents[index+2 : index+6])
		if !ok {
			output.Write(contents[index : index+2])
			index += 2
			continue
		}
		if unit == 0 {
			output.WriteString(`\u0000\u0000`)
			index += 6
			continue
		}
		if unit >= 0xd800 && unit <= 0xdbff && index+12 <= len(contents) &&
			contents[index+6] == '\\' && contents[index+7] == 'u' {
			low, lowOK := parseHexUnit(contents[index+8 : index+12])
			if lowOK && low >= 0xdc00 && low <= 0xdfff {
				output.Write(contents[index : index+12])
				index += 12
				continue
			}
		}
		if unit >= 0xd800 && unit <= 0xdfff {
			output.WriteString(`\u0000s`)
			output.WriteString(fmt.Sprintf("%04x", unit))
			index += 6
			continue
		}
		output.Write(contents[index : index+6])
		index += 6
	}
	return output.Bytes()
}

func constantAt(value []byte) (string, int, bool) {
	for _, token := range []string{"-Infinity", "Infinity", "NaN"} {
		if len(value) < len(token) || string(value[:len(token)]) != token {
			continue
		}
		if len(value) > len(token) {
			next := value[len(token)]
			if next == '_' || next >= '0' && next <= '9' || next >= 'A' && next <= 'Z' || next >= 'a' && next <= 'z' {
				continue
			}
		}
		return token, len(token), true
	}
	return "", 0, false
}

func parseHexUnit(value []byte) (uint16, bool) {
	if len(value) != 4 {
		return 0, false
	}
	parsed, err := strconv.ParseUint(string(value), 16, 16)
	return uint16(parsed), err == nil
}

func restore(value any) (any, error) {
	switch typed := value.(type) {
	case string:
		if len(typed) >= 2 && typed[0] == 0 && typed[1] == 'f' {
			switch typed[2:] {
			case "NaN":
				return NaN{}, nil
			case "Infinity":
				return Float(math.Inf(1)), nil
			case "-Infinity":
				return Float(math.Inf(-1)), nil
			}
		}
		return restoreString(typed), nil
	case json.Number:
		if _, ok := Integer(typed); ok {
			return typed, nil
		}
		parsed, err := strconv.ParseFloat(string(typed), 64)
		if err != nil {
			if numberError, ok := err.(*strconv.NumError); !ok || numberError.Err != strconv.ErrRange {
				return nil, err
			}
		}
		return Float(parsed), nil
	case []any:
		for index, item := range typed {
			restored, err := restore(item)
			if err != nil {
				return nil, err
			}
			typed[index] = restored
		}
		return typed, nil
	case map[string]any:
		restoredMap := make(map[string]any, len(typed))
		for key, item := range typed {
			restored, err := restore(item)
			if err != nil {
				return nil, err
			}
			restoredMap[restoreString(key)] = restored
		}
		return restoredMap, nil
	default:
		return typed, nil
	}
}

func restoreString(value string) string {
	if strings.IndexByte(value, 0) < 0 {
		return value
	}
	var output bytes.Buffer
	for index := 0; index < len(value); {
		if value[index] != 0 {
			output.WriteByte(value[index])
			index++
			continue
		}
		if index+1 < len(value) && value[index+1] == 0 {
			output.WriteByte(0)
			index += 2
			continue
		}
		if index+6 <= len(value) && value[index+1] == 's' {
			unit, ok := parseHexUnit([]byte(value[index+2 : index+6]))
			if ok {
				output.WriteByte(byte(0xe0 | unit>>12))
				output.WriteByte(byte(0x80 | unit>>6&0x3f))
				output.WriteByte(byte(0x80 | unit&0x3f))
				index += 6
				continue
			}
		}
		output.WriteByte(0)
		index++
	}
	return output.String()
}
