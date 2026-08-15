package runstate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

type jsonObject map[string]any
type pythonFloat float64
type pythonNaNConstant struct{}

const pythonIntegerDigitLimit = 4300

func decodeJSONObject(contents []byte) (jsonObject, error) {
	if !utf8.Valid(contents) {
		return nil, fmt.Errorf("invalid UTF-8")
	}
	protected, err := protectLoneSurrogates(contents)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(protected))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("multiple JSON values")
		}
		return nil, err
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("JSON value is not an object")
	}
	restored, err := restoreProtectedJSON(object)
	if err != nil {
		return nil, err
	}
	return jsonObject(restored.(map[string]any)), nil
}

// protectLoneSurrogates keeps encoding/json from replacing an escaped lone
// surrogate with U+FFFD. Raw non-UTF-8 remains rejected before this step.
func protectLoneSurrogates(contents []byte) ([]byte, error) {
	var output bytes.Buffer
	insideString := false
	for index := 0; index < len(contents); {
		current := contents[index]
		if !insideString {
			if token, width, ok := pythonConstantAt(contents[index:]); ok {
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
	return output.Bytes(), nil
}

func pythonConstantAt(value []byte) (string, int, bool) {
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

func restoreProtectedJSON(value any) (any, error) {
	switch typed := value.(type) {
	case string:
		if len(typed) >= 2 && typed[0] == 0 && typed[1] == 'f' {
			switch typed[2:] {
			case "NaN":
				return pythonNaNConstant{}, nil
			case "Infinity":
				return pythonFloat(math.Inf(1)), nil
			case "-Infinity":
				return pythonFloat(math.Inf(-1)), nil
			}
		}
		return restoreProtectedString(typed), nil
	case json.Number:
		if integer, ok := isJSONInteger(typed); ok {
			digits := len(integer)
			if len(integer) != 0 && integer[0] == '-' {
				digits--
			}
			if digits > pythonIntegerDigitLimit {
				return nil, &RuntimeError{message: "JSON integer token exceeds the frozen Python runtime limit of 4300 digits."}
			}
			return typed, nil
		}
		parsed, err := strconv.ParseFloat(string(typed), 64)
		if err != nil {
			if numberError, ok := err.(*strconv.NumError); !ok || numberError.Err != strconv.ErrRange {
				return nil, err
			}
		}
		return pythonFloat(parsed), nil
	case []any:
		for index, item := range typed {
			restored, err := restoreProtectedJSON(item)
			if err != nil {
				return nil, err
			}
			typed[index] = restored
		}
		return typed, nil
	case map[string]any:
		restoredMap := make(map[string]any, len(typed))
		for key, item := range typed {
			if isPythonConstantMarker(key) {
				return nil, fmt.Errorf("Python JSON constant cannot be an object key")
			}
			restoredKey := restoreProtectedString(key)
			restored, err := restoreProtectedJSON(item)
			if err != nil {
				return nil, err
			}
			restoredMap[restoredKey] = restored
		}
		return restoredMap, nil
	default:
		return typed, nil
	}
}

func isPythonConstantMarker(value string) bool {
	if len(value) < 2 || value[0] != 0 || value[1] != 'f' {
		return false
	}
	switch value[2:] {
	case "NaN", "Infinity", "-Infinity":
		return true
	default:
		return false
	}
}

func restoreProtectedString(value string) string {
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
				appendSurrogate(&output, unit)
				index += 6
				continue
			}
		}
		output.WriteByte(0)
		index++
	}
	return output.String()
}

func appendSurrogate(output *bytes.Buffer, unit uint16) {
	output.WriteByte(byte(0xe0 | unit>>12))
	output.WriteByte(byte(0x80 | unit>>6&0x3f))
	output.WriteByte(byte(0x80 | unit&0x3f))
}

func cloneJSON(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		cloned := make(map[string]any, len(typed))
		for key, item := range typed {
			cloned[key] = cloneJSON(item)
		}
		return cloned
	case jsonObject:
		cloned := make(jsonObject, len(typed))
		for key, item := range typed {
			cloned[key] = cloneJSON(item)
		}
		return cloned
	case []any:
		cloned := make([]any, len(typed))
		for index, item := range typed {
			cloned[index] = cloneJSON(item)
		}
		return cloned
	default:
		return typed
	}
}

func jsonEqual(left, right any) bool {
	if equal, numeric := pythonNumbersEqual(left, right); numeric {
		return equal
	}
	switch leftTyped := left.(type) {
	case nil:
		return right == nil
	case string:
		rightTyped, ok := right.(string)
		return ok && leftTyped == rightTyped
	case []any:
		rightTyped, ok := right.([]any)
		if !ok || len(leftTyped) != len(rightTyped) {
			return false
		}
		for index := range leftTyped {
			if !jsonEqual(leftTyped[index], rightTyped[index]) {
				return false
			}
		}
		return true
	case map[string]any:
		rightTyped, ok := right.(map[string]any)
		if !ok || len(leftTyped) != len(rightTyped) {
			return false
		}
		for key, value := range leftTyped {
			other, present := rightTyped[key]
			if !present || !jsonEqual(value, other) {
				return false
			}
		}
		return true
	case jsonObject:
		return jsonEqual(map[string]any(leftTyped), right)
	default:
		return false
	}
}

func pythonNumeric(value any) (*big.Int, float64, bool, bool) {
	switch typed := value.(type) {
	case bool:
		if typed {
			return big.NewInt(1), 0, true, true
		}
		return big.NewInt(0), 0, true, true
	case json.Number:
		integer, ok := isJSONInteger(typed)
		if !ok {
			return nil, 0, false, false
		}
		parsed, _ := new(big.Int).SetString(string(integer), 10)
		return parsed, 0, true, true
	case pythonFloat:
		return nil, float64(typed), false, true
	case pythonNaNConstant:
		return nil, math.NaN(), false, true
	default:
		return nil, 0, false, false
	}
}

func pythonNumbersEqual(left, right any) (bool, bool) {
	_, leftNaNConstant := left.(pythonNaNConstant)
	_, rightNaNConstant := right.(pythonNaNConstant)
	if leftNaNConstant && rightNaNConstant {
		return true, true
	}
	leftInteger, leftFloat, leftIsInteger, leftOK := pythonNumeric(left)
	rightInteger, rightFloat, rightIsInteger, rightOK := pythonNumeric(right)
	if !leftOK && !rightOK {
		return false, false
	}
	if !leftOK || !rightOK {
		return false, true
	}
	if leftIsInteger && rightIsInteger {
		return leftInteger.Cmp(rightInteger) == 0, true
	}
	if !leftIsInteger && !rightIsInteger {
		return leftFloat == rightFloat, true
	}
	integer := leftInteger
	floating := rightFloat
	if !leftIsInteger {
		integer = rightInteger
		floating = leftFloat
	}
	if math.IsNaN(floating) || math.IsInf(floating, 0) {
		return false, true
	}
	rational := new(big.Rat).SetFloat64(floating)
	return rational != nil && rational.Cmp(new(big.Rat).SetInt(integer)) == 0, true
}

func isJSONInteger(value any) (json.Number, bool) {
	number, ok := value.(json.Number)
	if !ok {
		return "", false
	}
	for _, character := range string(number) {
		if character == '.' || character == 'e' || character == 'E' {
			return "", false
		}
	}
	if _, ok := new(big.Int).SetString(string(number), 10); !ok {
		return "", false
	}
	return number, true
}

func integerEquals(value any, expected int64) bool {
	number, ok := isJSONInteger(value)
	if !ok {
		return false
	}
	parsed, _ := new(big.Int).SetString(string(number), 10)
	return parsed.Cmp(big.NewInt(expected)) == 0
}

func positiveInteger(value any) bool {
	number, ok := isJSONInteger(value)
	if !ok {
		return false
	}
	parsed, _ := new(big.Int).SetString(string(number), 10)
	return parsed.Sign() > 0
}

func nonNegativeInteger(value any) bool {
	number, ok := isJSONInteger(value)
	if !ok {
		return false
	}
	parsed, _ := new(big.Int).SetString(string(number), 10)
	return parsed.Sign() >= 0
}

func nonNegativeNumber(value any) bool {
	switch typed := value.(type) {
	case json.Number:
		integer, ok := isJSONInteger(typed)
		if !ok {
			return false
		}
		parsed, _ := new(big.Int).SetString(string(integer), 10)
		return parsed.Sign() >= 0
	case pythonFloat:
		return !(float64(typed) < 0)
	case pythonNaNConstant:
		return true
	default:
		return false
	}
}

func exactKeys(value map[string]any, expected ...string) bool {
	if len(value) != len(expected) {
		return false
	}
	for _, key := range expected {
		if _, ok := value[key]; !ok {
			return false
		}
	}
	return true
}

func canonicalJSON(value any, asciiOnly bool) ([]byte, error) {
	return canonicalJSONMode(value, asciiOnly, false)
}

func canonicalJSONMode(value any, asciiOnly, rawSurrogateEscape bool) ([]byte, error) {
	var output bytes.Buffer
	if err := appendCanonicalJSON(&output, value, asciiOnly, rawSurrogateEscape); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func prettyCanonicalJSONMode(value any, asciiOnly, rawSurrogateEscape bool) ([]byte, error) {
	var output bytes.Buffer
	if err := appendPrettyJSON(&output, value, asciiOnly, rawSurrogateEscape, 0); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func appendPrettyJSON(output *bytes.Buffer, value any, asciiOnly, rawSurrogateEscape bool, depth int) error {
	indent := func(level int) {
		output.WriteString(strings.Repeat(" ", level*2))
	}
	switch typed := value.(type) {
	case []any:
		if len(typed) == 0 {
			output.WriteString("[]")
			return nil
		}
		output.WriteString("[\n")
		for index, item := range typed {
			indent(depth + 1)
			if err := appendPrettyJSON(output, item, asciiOnly, rawSurrogateEscape, depth+1); err != nil {
				return err
			}
			if index != len(typed)-1 {
				output.WriteByte(',')
			}
			output.WriteByte('\n')
		}
		indent(depth)
		output.WriteByte(']')
		return nil
	case map[string]any:
		if len(typed) == 0 {
			output.WriteString("{}")
			return nil
		}
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		output.WriteString("{\n")
		for index, key := range keys {
			indent(depth + 1)
			if err := appendJSONString(output, key, asciiOnly, rawSurrogateEscape); err != nil {
				return err
			}
			output.WriteString(": ")
			if err := appendPrettyJSON(output, typed[key], asciiOnly, rawSurrogateEscape, depth+1); err != nil {
				return err
			}
			if index != len(keys)-1 {
				output.WriteByte(',')
			}
			output.WriteByte('\n')
		}
		indent(depth)
		output.WriteByte('}')
		return nil
	case jsonObject:
		return appendPrettyJSON(output, map[string]any(typed), asciiOnly, rawSurrogateEscape, depth)
	default:
		return appendCanonicalJSON(output, typed, asciiOnly, rawSurrogateEscape)
	}
}

func appendCanonicalJSON(output *bytes.Buffer, value any, asciiOnly, rawSurrogateEscape bool) error {
	switch typed := value.(type) {
	case nil:
		output.WriteString("null")
	case bool:
		if typed {
			output.WriteString("true")
		} else {
			output.WriteString("false")
		}
	case string:
		if err := appendJSONString(output, typed, asciiOnly, rawSurrogateEscape); err != nil {
			return err
		}
	case json.Number:
		if integer, ok := isJSONInteger(typed); ok {
			normalized, _ := new(big.Int).SetString(string(integer), 10)
			output.WriteString(normalized.String())
			return nil
		}
		output.WriteString(string(typed))
	case pythonFloat:
		output.WriteString(formatPythonFloat(float64(typed)))
	case pythonNaNConstant:
		output.WriteString("NaN")
	case int:
		output.WriteString(fmt.Sprintf("%d", typed))
	case int8:
		output.WriteString(fmt.Sprintf("%d", typed))
	case int16:
		output.WriteString(fmt.Sprintf("%d", typed))
	case int32:
		output.WriteString(fmt.Sprintf("%d", typed))
	case int64:
		output.WriteString(fmt.Sprintf("%d", typed))
	case uint:
		output.WriteString(fmt.Sprintf("%d", typed))
	case uint8:
		output.WriteString(fmt.Sprintf("%d", typed))
	case uint16:
		output.WriteString(fmt.Sprintf("%d", typed))
	case uint32:
		output.WriteString(fmt.Sprintf("%d", typed))
	case uint64:
		output.WriteString(fmt.Sprintf("%d", typed))
	case []any:
		output.WriteByte('[')
		for index, item := range typed {
			if index != 0 {
				output.WriteByte(',')
			}
			if err := appendCanonicalJSON(output, item, asciiOnly, rawSurrogateEscape); err != nil {
				return err
			}
		}
		output.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		output.WriteByte('{')
		for index, key := range keys {
			if index != 0 {
				output.WriteByte(',')
			}
			if err := appendJSONString(output, key, asciiOnly, rawSurrogateEscape); err != nil {
				return err
			}
			output.WriteByte(':')
			if err := appendCanonicalJSON(output, typed[key], asciiOnly, rawSurrogateEscape); err != nil {
				return err
			}
		}
		output.WriteByte('}')
	case jsonObject:
		return appendCanonicalJSON(output, map[string]any(typed), asciiOnly, rawSurrogateEscape)
	default:
		return fmt.Errorf("unsupported canonical JSON value %T", value)
	}
	return nil
}

func formatPythonFloat(value float64) string {
	switch {
	case math.IsNaN(value):
		return "NaN"
	case math.IsInf(value, 1):
		return "Infinity"
	case math.IsInf(value, -1):
		return "-Infinity"
	}
	formatted := strconv.FormatFloat(value, 'g', -1, 64)
	if !strings.ContainsAny(formatted, ".eE") {
		formatted += ".0"
	}
	return formatted
}

func appendJSONString(output *bytes.Buffer, value string, asciiOnly, rawSurrogateEscape bool) error {
	const hexadecimal = "0123456789abcdef"
	output.WriteByte('"')
	for index := 0; index < len(value); {
		if unit, width, ok := encodedSurrogate(value[index:]); ok {
			if rawSurrogateEscape && unit >= 0xdc80 && unit <= 0xdcff {
				output.WriteByte(byte(unit - 0xdc00))
			} else if rawSurrogateEscape {
				return fmt.Errorf("cannot encode unsupported lone surrogate \\u%04x", unit)
			} else if asciiOnly {
				writeUnicodeEscape(output, rune(unit), hexadecimal)
			} else {
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
			if character < 0x20 || asciiOnly && character > 0x7e {
				writeUnicodeEscape(output, character, hexadecimal)
			} else {
				output.WriteRune(character)
			}
		}
	}
	output.WriteByte('"')
	return nil
}

func encodedSurrogate(value string) (uint16, int, bool) {
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
