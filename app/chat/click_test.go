package chat

import (
	"testing"

	"github.com/gotd/td/tg"
)

func TestValidateClickOptions(t *testing.T) {
	tests := []struct {
		name    string
		opts    ClickOptions
		wantErr bool
	}{
		{
			name:    "missing source",
			opts:    ClickOptions{Text: "x"},
			wantErr: true,
		},
		{
			name:    "latest bot without chat",
			opts:    ClickOptions{LatestBot: true, Text: "x"},
			wantErr: true,
		},
		{
			name:    "latest bot with url conflict",
			opts:    ClickOptions{LatestBot: true, URL: "https://t.me/a/1", Chat: "bot", Text: "x"},
			wantErr: true,
		},
		{
			name:    "row without col",
			opts:    ClickOptions{URL: "https://t.me/a/1", Row: 1},
			wantErr: true,
		},
		{
			name:    "col without row",
			opts:    ClickOptions{URL: "https://t.me/a/1", Col: 1},
			wantErr: true,
		},
		{
			name:    "row col invalid index",
			opts:    ClickOptions{URL: "https://t.me/a/1", Row: 0, Col: 1},
			wantErr: true,
		},
		{
			name:    "no selector",
			opts:    ClickOptions{URL: "https://t.me/a/1"},
			wantErr: true,
		},
		{
			name:    "text mode",
			opts:    ClickOptions{URL: "https://t.me/a/1", Text: "go"},
			wantErr: false,
		},
		{
			name:    "latest bot mode",
			opts:    ClickOptions{LatestBot: true, Chat: "tag_access_bot", Text: "go"},
			wantErr: false,
		},
		{
			name:    "inspect mode valid",
			opts:    ClickOptions{LatestBot: true, Chat: "tag_access_bot", Inspect: true},
			wantErr: false,
		},
		{
			name:    "inspect requires latest bot",
			opts:    ClickOptions{URL: "https://t.me/a/1", Inspect: true},
			wantErr: true,
		},
		{
			name:    "inspect requires chat",
			opts:    ClickOptions{LatestBot: true, Inspect: true},
			wantErr: true,
		},
		{
			name:    "row col mode",
			opts:    ClickOptions{URL: "https://t.me/a/1", Row: 1, Col: 2},
			wantErr: false,
		},
		{
			name:    "row col and text allowed",
			opts:    ClickOptions{URL: "https://t.me/a/1", Row: 1, Col: 1, Text: "x"},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			err := tt.opts.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestSelectButtonByRowCol(t *testing.T) {
	rows := []tg.KeyboardButtonRow{
		{
			Buttons: []tg.KeyboardButtonClass{
				&tg.KeyboardButtonURL{Text: "web", URL: "https://example.com"},
				&tg.KeyboardButtonCallback{Text: "ok", Data: []byte("1")},
			},
		},
	}

	sel, warnings, err := selectButton(rows, ClickOptions{Row: 1, Col: 1, Text: "ok"})
	if err != nil {
		t.Fatalf("selectButton() error = %v", err)
	}
	if len(warnings) != 1 {
		t.Fatalf("expected one warning, got %d", len(warnings))
	}
	if !sel.skipped {
		t.Fatalf("expected skipped selection for unsupported type")
	}
	if sel.row != 1 || sel.col != 1 {
		t.Fatalf("unexpected selection: row=%d col=%d", sel.row, sel.col)
	}
}

func TestSelectButtonByTextSkipsUnsupported(t *testing.T) {
	rows := []tg.KeyboardButtonRow{
		{
			Buttons: []tg.KeyboardButtonClass{
				&tg.KeyboardButtonURL{Text: "same", URL: "https://example.com"},
				&tg.KeyboardButtonCallback{Text: "same", Data: []byte("payload")},
			},
		},
	}

	sel, _, err := selectButton(rows, ClickOptions{Text: "same"})
	if err != nil {
		t.Fatalf("selectButton() error = %v", err)
	}
	if sel.skipped {
		t.Fatalf("did not expect skipped selection")
	}
	if _, ok := sel.button.(*tg.KeyboardButtonCallback); !ok {
		t.Fatalf("expected callback button, got %T", sel.button)
	}
	if sel.col != 2 {
		t.Fatalf("expected second matching clickable button, got col=%d", sel.col)
	}
}

func TestSelectButtonByTextNoClickable(t *testing.T) {
	rows := []tg.KeyboardButtonRow{
		{
			Buttons: []tg.KeyboardButtonClass{
				&tg.KeyboardButtonURL{Text: "same", URL: "https://example.com"},
			},
		},
	}

	_, _, err := selectButton(rows, ClickOptions{Text: "same"})
	if err == nil {
		t.Fatal("expected error when all matched buttons are unsupported")
	}
}

func TestBuildBotCallbackRequest(t *testing.T) {
	peer := &tg.InputPeerSelf{}

	req, warnings, err := buildBotCallbackRequest(peer, 10, &tg.KeyboardButtonCallback{
		Text: "A",
		Data: []byte("orig"),
	}, ClickOptions{})
	if err != nil {
		t.Fatalf("buildBotCallbackRequest callback default error = %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings, got %v", warnings)
	}
	data, ok := req.GetData()
	if !ok || string(data) != "orig" {
		t.Fatalf("expected callback data orig, got ok=%v data=%q", ok, string(data))
	}

	req, warnings, err = buildBotCallbackRequest(peer, 11, &tg.KeyboardButtonCallback{
		Text: "A",
		Data: []byte("orig"),
	}, ClickOptions{Data: "override"})
	if err != nil {
		t.Fatalf("buildBotCallbackRequest callback override error = %v", err)
	}
	data, ok = req.GetData()
	if !ok || string(data) != "override" {
		t.Fatalf("expected overridden callback data, got ok=%v data=%q", ok, string(data))
	}

	req, warnings, err = buildBotCallbackRequest(peer, 12, &tg.KeyboardButtonGame{Text: "G"}, ClickOptions{Data: "ignored"})
	if err != nil {
		t.Fatalf("buildBotCallbackRequest game error = %v", err)
	}
	if !req.GetGame() {
		t.Fatal("expected game flag true")
	}
	if len(warnings) != 1 {
		t.Fatalf("expected one warning for data ignored, got %d", len(warnings))
	}
	if _, ok := req.GetData(); ok {
		t.Fatal("game request should not include callback data")
	}
}
