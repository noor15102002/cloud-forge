// Package jsoninput rejects ambiguous external JSON before schema decoding.
package jsoninput

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

const maximumDepth = 128

// Validate accepts exactly one JSON value with unique keys in every object.
// Callers must bound the input size. Errors never include arbitrary keys or
// values from the input, which may contain credentials or control characters.
func Validate(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := uniqueValue(decoder, 0); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("JSON must contain exactly one complete value")
	}
	return nil
}

func uniqueValue(decoder *json.Decoder, depth int) error {
	token, err := decoder.Token()
	if err != nil {
		return errors.New("JSON value is malformed or incomplete")
	}
	delimiter, compound := token.(json.Delim)
	if !compound {
		return nil
	}
	if depth >= maximumDepth {
		return errors.New("JSON nesting exceeds the supported depth")
	}
	switch delimiter {
	case '{':
		keys := make(map[string]struct{})
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return errors.New("JSON object is malformed or incomplete")
			}
			name, ok := key.(string)
			if !ok {
				return errors.New("JSON object key must be a string")
			}
			if _, exists := keys[name]; exists {
				return errors.New("duplicate JSON object key")
			}
			keys[name] = struct{}{}
			if err := uniqueValue(decoder, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := uniqueValue(decoder, depth+1); err != nil {
				return err
			}
		}
	default:
		return errors.New("unexpected JSON delimiter")
	}
	if _, err := decoder.Token(); err != nil {
		return errors.New("JSON value is malformed or incomplete")
	}
	return nil
}
