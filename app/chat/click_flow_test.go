package chat

import (
	"testing"

	"github.com/gotd/td/tg"
)

func TestFlowConfigValidate(t *testing.T) {
	cfg := &FlowConfig{
		Target: targetLatestInboundButton,
		Selectors: []FlowSelector{
			{ID: "next", Text: "Next"},
		},
		Actions: FlowActions{
			Mode: actionModeLoop,
			Loop: []string{"next"},
		},
		MediaScope: FlowMediaScope{Mode: "since_start"},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() unexpected error: %v", err)
	}
}

func TestCompileSelectorsAndNumberMatch(t *testing.T) {
	sels, err := compileSelectors([]FlowSelector{
		{ID: "n1", Number: 1},
		{ID: "rx", Regex: `^➡️`},
		{ID: "txt", Text: "固定"},
	})
	if err != nil {
		t.Fatalf("compileSelectors() error: %v", err)
	}

	if !matchSelector(sels["n1"], &tg.KeyboardButtonCallback{Text: "· 1 ·", Data: []byte("x")}) {
		t.Fatal("number selector should match first digits")
	}
	if !matchSelector(sels["rx"], &tg.KeyboardButtonCallback{Text: "➡️ 下一页", Data: []byte("x")}) {
		t.Fatal("regex selector should match")
	}
	if !matchSelector(sels["txt"], &tg.KeyboardButtonCallback{Text: "固定", Data: []byte("x")}) {
		t.Fatal("text selector should match")
	}
}

func TestStopMatchedFinishButButtonStillPresent(t *testing.T) {
	sels, err := compileSelectors([]FlowSelector{
		{ID: "next_batch", Text: "➡️ 点击查看下一组"},
	})
	if err != nil {
		t.Fatalf("compileSelectors() error: %v", err)
	}

	snapWithNext := flowSnapshot{
		latestInbound: &tg.Message{
			Message: "✅ 当前批次发送完成",
		},
		latestInboundButton: &tg.Message{
			ReplyMarkup: &tg.ReplyInlineMarkup{
				Rows: []tg.KeyboardButtonRow{
					{Buttons: []tg.KeyboardButtonClass{
						&tg.KeyboardButtonCallback{Text: "➡️ 点击查看下一组", Data: []byte("a")},
					}},
				},
			},
		},
	}

	stop := FlowStopConditions{
		TextContains:     []string{"发送完成"},
		AllButtonsAbsent: []string{"next_batch"},
	}

	if stopMatched(stop, sels, snapWithNext, 0) {
		t.Fatal("should not stop when next-batch button still exists")
	}

	snapNoNext := snapWithNext
	snapNoNext.latestInboundButton = &tg.Message{
		ReplyMarkup: &tg.ReplyInlineMarkup{
			Rows: []tg.KeyboardButtonRow{
				{Buttons: []tg.KeyboardButtonClass{
					&tg.KeyboardButtonCallback{Text: "📁 文件夹详情", Data: []byte("b")},
				}},
			},
		},
	}

	if !stopMatched(stop, sels, snapNoNext, 0) {
		t.Fatal("should stop when finish text matched and next-batch button is absent")
	}
}

func TestClickFlowOptionsValidate(t *testing.T) {
	opts := ClickFlowOptions{
		Chat:   "bot",
		Flow:   "flow.yaml",
		Output: "out.json",
	}
	if err := opts.Validate(); err != nil {
		t.Fatalf("Validate() unexpected error: %v", err)
	}
}
