package pyjson

import (
	"errors"
	"math"
	"strings"
	"testing"
)

func TestFormatFloatMatchesPythonRepr(t *testing.T) {
	pointOne, pointTwo := 0.1, 0.2
	tests := []struct {
		value float64
		want  string
	}{
		{0, "0.0"},
		{math.Copysign(0, -1), "-0.0"},
		{1.5, "1.5"},
		{1e-4, "0.0001"},
		{1e-5, "1e-05"},
		{1e5, "100000.0"},
		{1e6, "1000000.0"},
		{1e9, "1000000000.0"},
		{1e15, "1000000000000000.0"},
		{1e16, "1e+16"},
		{123456789.123, "123456789.123"},
		{pointOne + pointTwo, "0.30000000000000004"},
		{math.MaxFloat64, "1.7976931348623157e+308"},
		{5e-324, "5e-324"},
		{math.Inf(1), "Infinity"},
		{math.Inf(-1), "-Infinity"},
		{math.NaN(), "NaN"},
	}
	for _, test := range tests {
		if got := FormatFloat(test.value); got != test.want {
			t.Errorf("FormatFloat(%v) = %q, want %q", test.value, got, test.want)
		}
	}
}

func TestDecodeEncodeRoundTripsPythonValues(t *testing.T) {
	value, err := Decode([]byte(`{"b":[1e9,-0,1E400,NaN,-Infinity],"a":"\udcff😀\u0000"}`))
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	compact, err := Encode(value, Options{ASCII: true})
	if err != nil {
		t.Fatalf("Encode(ASCII) error = %v", err)
	}
	want := `{"a":"\udcff` + "\\u" + "d83d" + "\\u" + "de00" + `\u0000","b":[1000000000.0,0,Infinity,NaN,-Infinity]}`
	if string(compact) != want {
		t.Fatalf("Encode(ASCII) = %s, want %s", compact, want)
	}
	pretty, err := Encode(value, Options{Indent: true, RawSurrogateBytes: true})
	if err != nil {
		t.Fatalf("Encode(raw) error = %v", err)
	}
	wantPretty := "{\n  \"a\": \"\xff\U0001f600\\u0000\",\n  \"b\": [\n    1000000000.0,\n    0,\n    Infinity,\n    NaN,\n    -Infinity\n  ]\n}"
	if string(pretty) != wantPretty {
		t.Fatalf("Encode(raw) = %q, want %q", pretty, wantPretty)
	}
	if _, err := Encode(value, Options{}); err == nil {
		t.Fatal("Encode(UTF-8) accepted a lone surrogate")
	}
}

func TestDecodeClassifiesPythonFailures(t *testing.T) {
	oversized := strings.Repeat("9", IntegerDigitLimit+1)
	tests := []struct {
		name  string
		input string
		check func(error) bool
	}{
		{"invalid UTF-8", "{\"a\":\"\xff\"}", func(err error) bool { return errors.Is(err, ErrInvalidUTF8) }},
		{"integer limit", `[` + oversized + `]`, func(err error) bool {
			var limit *IntegerLimitError
			return errors.As(err, &limit) && limit.Syntax == nil
		}},
		{"integer limit before syntax error", `[` + oversized + `,}`, func(err error) bool {
			var limit *IntegerLimitError
			return errors.As(err, &limit) && limit.Syntax != nil
		}},
		{"depth limit", strings.Repeat("[", 10001) + strings.Repeat("]", 10001), IsDepthLimit},
		{"constant key", `{NaN:1}`, func(err error) bool { return errors.Is(err, errConstantKey) }},
		{"trailing value", `{} {}`, func(err error) bool { return errors.Is(err, errMultipleValues) }},
		{"constant prefix", `[NaN1]`, func(err error) bool { return err != nil }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Decode([]byte(test.input)); !test.check(err) {
				t.Fatalf("Decode() error = %v", err)
			}
		})
	}
	if _, err := Decode([]byte(`[` + strings.Repeat("9", IntegerDigitLimit) + `, 1.` + oversized + `]`)); err != nil {
		t.Fatalf("Decode(limit and long fraction) error = %v", err)
	}
}

func TestEqualUsesPythonNumberSemantics(t *testing.T) {
	left, _ := Decode([]byte(`[1, 1.0, true, NaN, {"a": 2}]`))
	right, _ := Decode([]byte(`[1.0, true, 1, NaN, {"a": 2.0}]`))
	if !Equal(left, right) {
		t.Fatal("Equal() = false for Python-equal values")
	}
	floatNaN, _ := Decode([]byte(`[1E400]`))
	if Equal(floatNaN, []any{json1()}) {
		t.Fatal("Equal() matched infinity with an integer")
	}
}

func json1() any {
	value, _ := Decode([]byte(`1`))
	return value
}
