package mockllm

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type completion struct {
	Choices []struct {
		Message struct {
			ToolCalls []toolCall `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
}

func post(t *testing.T, srv *httptest.Server, body string) (int, string) {
	t.Helper()
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(
		srv.URL+"/v1/chat/completions",
		"application/json",
		strings.NewReader(body),
	)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp.StatusCode, string(data)
}

func TestListsModels(t *testing.T) {
	srv := httptest.NewServer(Handler())
	defer srv.Close()

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(srv.URL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

func TestMatrixPromptReturnsThemeEdits(t *testing.T) {
	srv := httptest.NewServer(Handler())
	defer srv.Close()

	status, body := post(t, srv, `{"messages":[{"role":"user","content":"make it matrix"}]}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d, body = %s", status, body)
	}

	var got completion
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	if len(got.Choices) != 1 {
		t.Fatalf("choices = %d, want 1", len(got.Choices))
	}
	if got.Choices[0].FinishReason != "tool_calls" {
		t.Errorf("finish_reason = %q", got.Choices[0].FinishReason)
	}

	calls := got.Choices[0].Message.ToolCalls
	if len(calls) != 1 || calls[0].Function.Name != "set_site" {
		t.Fatalf("want one set_site call, got %+v", calls)
	}
	var site struct {
		CSS   string `json:"css"`
		Board string `json:"board"`
	}
	if err := json.Unmarshal([]byte(calls[0].Function.Arguments), &site); err != nil {
		t.Fatalf("decode args: %v", err)
	}
	if !strings.Contains(site.CSS, "00ff6a") {
		t.Errorf("css is not the matrix palette: %s", site.CSS)
	}
	if !strings.Contains(site.Board, "{{posts}}") {
		t.Errorf("board lost the posts placeholder: %s", site.Board)
	}
}

// The edit keywords answer in one response: the edit, then done.
func TestEditKeywordsSendEditAndDoneTogether(t *testing.T) {
	srv := httptest.NewServer(Handler())
	defer srv.Close()

	for _, keyword := range []string{"mockedit", "mockpatch", "mockbadedit"} {
		status, raw := post(t, srv,
			`{"messages":[{"role":"user","content":"NEW POST: `+keyword+`"}]}`)
		if status != http.StatusOK {
			t.Fatalf("%s: status = %d", keyword, status)
		}
		var got completion
		if err := json.Unmarshal([]byte(raw), &got); err != nil {
			t.Fatalf("%s: decode: %v", keyword, err)
		}
		calls := got.Choices[0].Message.ToolCalls
		if len(calls) != 2 || calls[0].Function.Name != "edit" ||
			calls[1].Function.Name != "done" {
			t.Errorf("%s: want edit then done, got %+v", keyword, calls)
		}
	}
}

func TestToolResultClosesWithDone(t *testing.T) {
	srv := httptest.NewServer(Handler())
	defer srv.Close()

	body := `{"messages":[` +
		`{"role":"user","content":"make it matrix"},` +
		`{"role":"assistant","content":"","tool_calls":[]},` +
		`{"role":"tool","content":"ok","tool_call_id":"call_css"}]}`
	status, raw := post(t, srv, body)
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}

	var got completion
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	calls := got.Choices[0].Message.ToolCalls
	if len(calls) != 1 || calls[0].Function.Name != "done" {
		t.Fatalf("want a single done call, got %+v", calls)
	}
}

func TestMockfailReturns500(t *testing.T) {
	srv := httptest.NewServer(Handler())
	defer srv.Close()

	status, _ := post(t, srv, `{"messages":[{"role":"user","content":"mockfail"}]}`)
	if status != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", status)
	}
}

func TestUnknownPathIs404(t *testing.T) {
	srv := httptest.NewServer(Handler())
	defer srv.Close()

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(srv.URL + "/v1/embeddings")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestChaosPromptIncludesScript(t *testing.T) {
	srv := httptest.NewServer(Handler())
	defer srv.Close()

	status, raw := post(t, srv, `{"messages":[{"role":"user","content":"make chaos"}]}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}

	var got completion
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var header string
	for _, call := range got.Choices[0].Message.ToolCalls {
		if call.Function.Name != "set_site" {
			continue
		}
		var args struct {
			Header string `json:"header"`
		}
		if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
			t.Fatalf("decode args: %v", err)
		}
		header = args.Header
	}
	if !strings.Contains(header, "<script>") {
		t.Fatalf("chaos theme has no script: %q", header)
	}
}
