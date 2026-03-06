package cmd

import (
	"bytes"
	"strings"
	"testing"
)

func TestChatClickFlowCommandValidation(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			name:    "missing chat",
			args:    []string{"--flow", "flow.yaml"},
			wantErr: "missing --chat",
		},
		{
			name:    "missing flow",
			args:    []string{"--chat", "bot"},
			wantErr: "missing --flow",
		},
		{
			name:    "negative max steps",
			args:    []string{"--chat", "bot", "--flow", "flow.yaml", "--max-steps", "-1"},
			wantErr: "--max-steps must be >= 0",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			c := NewChatClickFlow()
			var stdout, stderr bytes.Buffer
			c.SetOut(&stdout)
			c.SetErr(&stderr)
			c.SetArgs(tt.args)

			err := c.Execute()
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("unexpected error: %v, want contains %q", err, tt.wantErr)
			}
		})
	}
}
