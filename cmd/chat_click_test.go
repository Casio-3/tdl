package cmd

import (
	"bytes"
	"strings"
	"testing"
)

func TestChatClickCommandValidation(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			name:    "missing source",
			args:    []string{"--text", "go"},
			wantErr: "either --url or --latest-bot must be provided",
		},
		{
			name:    "row without col",
			args:    []string{"--url", "https://t.me/a/1", "--row", "1"},
			wantErr: "--row and --col must be used together",
		},
		{
			name:    "col without row",
			args:    []string{"--url", "https://t.me/a/1", "--col", "1"},
			wantErr: "--row and --col must be used together",
		},
		{
			name:    "no selector",
			args:    []string{"--url", "https://t.me/a/1"},
			wantErr: "either --row/--col or --text must be provided",
		},
		{
			name:    "latest bot without chat",
			args:    []string{"--latest-bot", "--text", "go"},
			wantErr: "--chat must be provided when --latest-bot is set",
		},
		{
			name:    "latest bot conflict with url",
			args:    []string{"--latest-bot", "--chat", "tag_access_bot", "--url", "https://t.me/a/1", "--text", "go"},
			wantErr: "--url and --latest-bot cannot be used together",
		},
		{
			name:    "inspect requires latest bot",
			args:    []string{"--inspect", "--url", "https://t.me/a/1"},
			wantErr: "--inspect requires --latest-bot",
		},
		{
			name:    "inspect requires chat",
			args:    []string{"--inspect", "--latest-bot"},
			wantErr: "--inspect requires --chat",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			c := NewChatClick()
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
