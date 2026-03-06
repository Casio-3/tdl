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

func TestFlowConfigValidateForwardMode(t *testing.T) {
	cfg := &FlowConfig{
		Target: targetLatestInboundButton,
		Selectors: []FlowSelector{
			{ID: "next", Text: "Next"},
		},
		Actions: FlowActions{
			Mode: actionModeLoop,
			Loop: []string{"next"},
		},
		Forward: FlowForward{
			To:   "3786555826",
			Mode: "all_messages",
		},
		MediaScope: FlowMediaScope{Mode: "since_start"},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() unexpected error: %v", err)
	}
}

func TestFlowConfigValidateInvalidForwardMode(t *testing.T) {
	cfg := &FlowConfig{
		Target: targetLatestInboundButton,
		Selectors: []FlowSelector{
			{ID: "next", Text: "Next"},
		},
		Actions: FlowActions{
			Mode: actionModeLoop,
			Loop: []string{"next"},
		},
		Forward: FlowForward{
			To:   "3786555826",
			Mode: "bad_mode",
		},
		MediaScope: FlowMediaScope{Mode: "since_start"},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() expected error for invalid forward mode, got nil")
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

func TestResolveForward(t *testing.T) {
	fwd, err := resolveForward(FlowForward{
		To:   "1001",
		Mode: "media_only",
	}, ClickFlowOptions{})
	if err != nil {
		t.Fatalf("resolveForward() error: %v", err)
	}
	if fwd.to != "1001" || fwd.mode != "media_only" {
		t.Fatalf("unexpected forward: %+v", fwd)
	}

	fwd, err = resolveForward(FlowForward{
		To:   "1001",
		Mode: "media_only",
	}, ClickFlowOptions{ForwardTo: "3786555826"})
	if err != nil {
		t.Fatalf("resolveForward() with override error: %v", err)
	}
	if fwd.to != "3786555826" {
		t.Fatalf("expected override peer, got %s", fwd.to)
	}
}

func TestResolveForwardDefaultsAndInvalidMode(t *testing.T) {
	fwd, err := resolveForward(FlowForward{
		To: "1001",
	}, ClickFlowOptions{})
	if err != nil {
		t.Fatalf("resolveForward() default mode error: %v", err)
	}
	if fwd.mode != "media_only" {
		t.Fatalf("expected default mode media_only, got %s", fwd.mode)
	}

	_, err = resolveForward(FlowForward{
		To:   "1001",
		Mode: "invalid",
	}, ClickFlowOptions{})
	if err == nil {
		t.Fatal("resolveForward() expected invalid mode error, got nil")
	}
}

func TestMediaCollectorForwardIDs(t *testing.T) {
	m := newMediaCollector(100)
	msgs := []*tg.Message{
		{ID: 98, Out: false}, // baseline ignored
		{ID: 101, Out: false},
		{ID: 103, Out: false},
		{ID: 102, Out: false, Media: &tg.MessageMediaPhoto{}},
		{ID: 104, Out: false, Media: &tg.MessageMediaDocument{}},
		{ID: 105, Out: true, Media: &tg.MessageMediaPhoto{}}, // outbound ignored
	}
	m.consume(msgs)

	all := m.forwardIDs("all_messages")
	if len(all) != 4 || all[0] != 101 || all[1] != 102 || all[2] != 103 || all[3] != 104 {
		t.Fatalf("unexpected all_messages ids: %v", all)
	}

	mediaOnly := m.forwardIDs("media_only")
	if len(mediaOnly) != 2 || mediaOnly[0] != 102 || mediaOnly[1] != 104 {
		t.Fatalf("unexpected media_only ids: %v", mediaOnly)
	}
}
