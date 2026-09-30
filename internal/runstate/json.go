package runstate

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"

	"github.com/jgoneit/seal/internal/pyjson"
)

type jsonObject = map[string]any

// decodeJSONObject parses one stored JSON object with Python json.load
// semantics. The CPython integer limit is a runtime failure, as in the
// frozen Reference; other failures are left for the caller to classify.
func decodeJSONObject(contents []byte) (jsonObject, error) {
	value, err := pyjson.Decode(contents)
	var limit *pyjson.IntegerLimitError
	if errors.As(err, &limit) {
		return nil, &RuntimeError{message: limit.Error()}
	}
	if err != nil {
		return nil, err
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("JSON value is not an object")
	}
	return object, nil
}

func integerEquals(value any, expected int64) bool {
	number, ok := pyjson.Integer(value)
	if !ok {
		return false
	}
	parsed, _ := new(big.Int).SetString(string(number), 10)
	return parsed.Cmp(big.NewInt(expected)) == 0
}

func positiveInteger(value any) bool {
	number, ok := pyjson.Integer(value)
	if !ok {
		return false
	}
	parsed, _ := new(big.Int).SetString(string(number), 10)
	return parsed.Sign() > 0
}

func nonNegativeInteger(value any) bool {
	number, ok := pyjson.Integer(value)
	if !ok {
		return false
	}
	parsed, _ := new(big.Int).SetString(string(number), 10)
	return parsed.Sign() >= 0
}

func nonNegativeNumber(value any) bool {
	switch typed := value.(type) {
	case json.Number:
		integer, ok := pyjson.Integer(typed)
		if !ok {
			return false
		}
		parsed, _ := new(big.Int).SetString(string(integer), 10)
		return parsed.Sign() >= 0
	case pyjson.Float:
		return !(float64(typed) < 0)
	case pyjson.NaN:
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
