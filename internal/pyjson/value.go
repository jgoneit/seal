package pyjson

import (
	"encoding/json"
	"math"
	"math/big"
)

// Integer reports whether value is a decoded JSON integer token.
func Integer(value any) (json.Number, bool) {
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

// Clone deep-copies decoded objects and arrays.
func Clone(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		cloned := make(map[string]any, len(typed))
		for key, item := range typed {
			cloned[key] = Clone(item)
		}
		return cloned
	case []any:
		cloned := make([]any, len(typed))
		for index, item := range typed {
			cloned[index] = Clone(item)
		}
		return cloned
	default:
		return typed
	}
}

// Equal compares decoded values with Python == semantics, including
// bool/int/float numeric equality and NaN constant identity.
func Equal(left, right any) bool {
	if equal, numeric := numbersEqual(left, right); numeric {
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
			if !Equal(leftTyped[index], rightTyped[index]) {
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
			if !present || !Equal(value, other) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func numeric(value any) (*big.Int, float64, bool, bool) {
	switch typed := value.(type) {
	case bool:
		if typed {
			return big.NewInt(1), 0, true, true
		}
		return big.NewInt(0), 0, true, true
	case json.Number:
		integer, ok := Integer(typed)
		if !ok {
			return nil, 0, false, false
		}
		parsed, _ := new(big.Int).SetString(string(integer), 10)
		return parsed, 0, true, true
	case Float:
		return nil, float64(typed), false, true
	case NaN:
		return nil, math.NaN(), false, true
	default:
		return nil, 0, false, false
	}
}

func numbersEqual(left, right any) (bool, bool) {
	_, leftNaN := left.(NaN)
	_, rightNaN := right.(NaN)
	if leftNaN && rightNaN {
		return true, true
	}
	leftInteger, leftFloat, leftIsInteger, leftOK := numeric(left)
	rightInteger, rightFloat, rightIsInteger, rightOK := numeric(right)
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
