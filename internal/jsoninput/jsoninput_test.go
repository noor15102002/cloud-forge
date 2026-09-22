package jsoninput

import (
	"strings"
	"testing"
)

func TestValidateRejectsAmbiguousIncompleteAndDeepJSON(t *testing.T) {
	for name, input := range map[string]string{
		"duplicate":            `{"status":"pass","status":"fail"}`,
		"escaped duplicate":    `{"st\u0061tus":"pass","status":"fail"}`,
		"nested duplicate":     `{"results":[{"private-secret":1,"private-secret":2}]}`,
		"truncated":            `{"results":[{"value":`,
		"mismatched delimiter": `{"results":[1}}`,
		"second value":         `{} {}`,
		"trailing data":        `{}private-secret`,
		"empty":                ``,
		"depth":                strings.Repeat("[", maximumDepth+1) + "0" + strings.Repeat("]", maximumDepth+1),
	} {
		t.Run(name, func(t *testing.T) {
			err := Validate([]byte(input))
			if err == nil {
				t.Fatal("untrustworthy JSON was accepted")
			}
			if strings.Contains(err.Error(), "private-secret") {
				t.Fatal("error exposed untrusted input")
			}
		})
	}
}

func TestValidateKeepsSeparateObjectScopesAndJSONValues(t *testing.T) {
	for _, input := range []string{
		`{"results":[{"value":1},{"value":2}],"value":null}`,
		`[true,false,null,"text",-1.2e3,{},[]]`,
		strings.Repeat("[", maximumDepth) + "0" + strings.Repeat("]", maximumDepth),
	} {
		if err := Validate([]byte(input)); err != nil {
			t.Fatalf("valid JSON rejected: %v", err)
		}
	}
}
