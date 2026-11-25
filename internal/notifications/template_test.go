package notifications

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRenderTextAndJSON(t *testing.T) {
	event := Event{Host: "host\"\\\n\u65e5", Job: "$status", Status: "failed", Output: "first\r\nsecond\r\nthird\r\n"}
	plain, err := Render("$host|$job|$status|$output:2|$$host", "text/plain", event)
	if err != nil || plain != event.Host+"|$status|failed|second\nthird|$host" {
		t.Fatalf("single pass substitution: %q %v", plain, err)
	}
	rendered, err := Render(`{"host":"$host","job":"$job","output":"$output:2"}`, "application/json", event)
	var values map[string]string
	if err != nil || json.Unmarshal([]byte(rendered), &values) != nil || values["host"] != event.Host || values["output"] != "second\nthird" || values["job"] != "$status" {
		t.Fatalf("unsafe JSON substitution: %q %v", rendered, err)
	}
	for _, count := range []string{"1", "10", "1000"} {
		if _, err := Render("$output:"+count, "text/plain", event); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRenderRejectsInvalidTemplates(t *testing.T) {
	for _, body := range []string{"$missing", "$", "$hostile", "$output:", "$output:0", "$output:-1", "$output:1001", "$output:999999999999999999999"} {
		if _, err := Render(body, "text/plain", Event{}); err == nil {
			t.Errorf("accepted %q", body)
		}
	}
	for _, body := range []string{`{"output":$output}`, `{"text":"$host}`, `{"text":"\$host"}`} {
		if _, err := Render(body, "application/json", Event{Output: "value"}); err == nil {
			t.Errorf("accepted invalid JSON %q", body)
		}
	}
	if _, err := Render(strings.Repeat("$output", 5), "text/plain", Event{Output: strings.Repeat("x", 256*1024)}); err == nil {
		t.Fatal("unbounded rendered body")
	}
}

func TestTailLines(t *testing.T) {
	for _, item := range []struct {
		output string
		count  int
		want   string
	}{
		{"", 10, ""}, {"one", 1, "one"}, {"one\n", 1, "one"}, {"one\ntwo\n", 1, "two"}, {"one\n\n", 1, ""}, {"one\ntwo", 10, "one\ntwo"}, {"one\r\ntwo\r\n", 1, "two"},
	} {
		if got := tailLines(item.output, item.count); got != item.want {
			t.Errorf("tail %q: %q, want %q", item.output, got, item.want)
		}
	}
}

func TestJSONTemplateCannotChangeStructureOrCompleteEscapes(t *testing.T) {
	for _, body := range []string{`[1$output]`, `{"value":$token}`, `{"value":"\u00$token"}`, `{"value":"\$token"}`} {
		for _, event := range []Event{{}, {Output: ",2", Secrets: map[string]string{"token": "41"}}} {
			if _, err := Render(body, "application/json", event); err == nil {
				t.Errorf("accepted unsafe JSON template %q", body)
			}
		}
	}
	// Static escaped backslashes and dollar literals must still work.
	rendered, err := Render(`{"value":"\\$output $$"}`, "application/json", Event{Output: `",2`})
	var decoded map[string]string
	if err != nil || json.Unmarshal([]byte(rendered), &decoded) != nil || decoded["value"] != "\\\",2 $" {
		t.Fatalf("valid escaped template failed: %q %v", rendered, err)
	}
}

func TestRenderedBodyLimitIncludesTrailingLiterals(t *testing.T) {
	event := Event{Output: strings.Repeat("x", 256*1024)}
	boundary := strings.Repeat("$output", 4)
	if body, err := Render(boundary, "text/plain", event); err != nil || len(body) != 1024*1024 {
		t.Fatalf("body at limit rejected: %v", err)
	}
	for _, tail := range []string{"x", "$$"} {
		if _, err := Render(boundary+tail, "text/plain", event); err == nil {
			t.Fatal("trailing literals bypassed the rendered body limit")
		}
	}
}

func TestNotificationTemplatesIgnoreNameCaseAndPreserveValues(t *testing.T) {
	event := Event{Host: "HostName", Job: "JobName", Status: "FaIlEd", Output: "first\nLaSt\n", Secrets: map[string]string{"Api_Token": "MiXeD-$HOST"}}
	rendered, err := Render("$HOST|$jOb|$StAtUs|$OUTPUT:1|$api_TOKEN|$$HoSt", "text/plain", event)
	if err != nil || rendered != "HostName|JobName|FaIlEd|LaSt|MiXeD-$HOST|$HoSt" {
		t.Fatalf("case-insensitive names changed literal values: %q %v", rendered, err)
	}
	endpoint, err := renderEndpoint(Endpoint{URL: "https://Example.internal/Keep/$API_TOKEN?Key=$api_token", Headers: map[string]string{"Authorization": "Bearer $Api_ToKeN"}}, event.Secrets)
	if err != nil || endpoint.Headers["Authorization"] != "Bearer MiXeD-$HOST" || !strings.HasPrefix(endpoint.URL, "https://Example.internal/Keep/") || !strings.Contains(endpoint.URL, "?Key=") {
		t.Fatalf("endpoint names or literal casing changed: %+v %v", endpoint, err)
	}
}
