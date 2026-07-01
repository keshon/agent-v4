package tools

import (
	"context"
	"encoding/json"
	"testing"
)

func TestAskUser_CallsAskFnAndReturnsAnswer(t *testing.T) {
	tool := AskUser{AskFn: func(question string) (string, error) {
		if question != "what color?" {
			t.Errorf("got question %q, want %q", question, "what color?")
		}
		return "blue", nil
	}}
	args, _ := json.Marshal(map[string]string{"question": "what color?"})
	out, err := tool.Run(context.Background(), args)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out != "blue" {
		t.Fatalf("got %q, want %q", out, "blue")
	}
}

func TestAskUser_NilAskFnReturnsError(t *testing.T) {
	tool := AskUser{} // no AskFn configured
	args, _ := json.Marshal(map[string]string{"question": "hello?"})
	if _, err := tool.Run(context.Background(), args); err == nil {
		t.Fatal("expected an error when AskFn is nil")
	}
}

func TestAskUser_ModeIsExclusive(t *testing.T) {
	var tool AskUser
	if tool.Mode().String() != "Exclusive" {
		t.Fatalf("Mode() = %q, want Exclusive", tool.Mode())
	}
}
