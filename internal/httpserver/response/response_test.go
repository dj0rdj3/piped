package response

import (
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDecodeLimitPreservesStrictJSONAndMediaTypeRules(t *testing.T) {
	for _, fixture := range []struct {
		name, media, body string
		limit             int64
		status            int
	}{
		{"object", "application/json", `{"value":"ok"}`, 100, 0},
		{"charset", "application/json; charset=utf-8", `{"value":"ok"}`, 100, 0},
		{"surrounding whitespace", "application/json", " \t{}\r\n", 100, 0},
		{"existing null behavior", "application/json", "null", 100, 0},
		{"exact bound", "application/json", "{}", 2, 0},
		{"too small", "application/json", "{}", 1, 400},
		{"whitespace exceeds bound", "application/json", "{}  ", 3, 400},
		{"missing media type", "", "{}", 100, 415},
		{"wrong media type", "text/plain", "{}", 100, 415},
		{"invalid media parameter", "application/json; charset=\"", "{}", 100, 415},
		{"empty", "application/json", "", 100, 400},
		{"malformed", "application/json", "{", 100, 400},
		{"wrong field type", "application/json", `{"value":[]}`, 100, 400},
		{"unknown field", "application/json", `{"private_input":"must-not-be-echoed"}`, 100, 400},
		{"second object", "application/json", "{} {}", 100, 400},
		{"second array", "application/json", "{} []", 100, 400},
		{"second string", "application/json", `{} "must-not-be-echoed"`, 100, 400},
		{"second number", "application/json", "{} 123", 100, 400},
		{"second boolean", "application/json", "{} true", 100, 400},
		{"second null", "application/json", "{} null", 100, 400},
		{"trailing garbage", "application/json", "{} must-not-be-echoed", 100, 400},
		{"trailing comma", "application/json", "{},", 100, 400},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			request := httptest.NewRequest("POST", "/", strings.NewReader(fixture.body))
			request.Header.Set("Content-Type", fixture.media)
			var target struct {
				Value string `json:"value"`
			}
			err := DecodeLimit(httptest.NewRecorder(), request, &target, fixture.limit)
			if fixture.status == 0 {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var public *Error
			if !errors.As(err, &public) || public.Status != fixture.status || strings.Contains(public.Message, "must-not-be-echoed") {
				t.Fatalf("unexpected public decoding error: %v", err)
			}
		})
	}
}

type countingReader struct {
	io.Reader
	bytes int
}

func (r *countingReader) Read(data []byte) (int, error) {
	count, err := r.Reader.Read(data)
	r.bytes += count
	return count, err
}

func TestDecodeRejectsTrailingStructuresWithoutParsingTheirContents(t *testing.T) {
	body := `{}` + ` [` + strings.Repeat(`{"value":"unused"},`, 50000) + `{}]`
	input := &countingReader{Reader: strings.NewReader(body)}
	request := httptest.NewRequest("POST", "/", input)
	request.Header.Set("Content-Type", "application/json")
	var target struct{}
	err := DecodeLimit(httptest.NewRecorder(), request, &target, int64(len(body)))
	var public *Error
	if !errors.As(err, &public) || public.Message != "request must contain one JSON object" {
		t.Fatalf("trailing structure accepted: %v", err)
	}
	if input.bytes >= len(body)/2 {
		t.Fatalf("parsed a rejected trailing structure: consumed %d of %d bytes", input.bytes, len(body))
	}
}
