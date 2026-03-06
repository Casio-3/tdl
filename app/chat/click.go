package chat

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/telegram/query"
	"github.com/gotd/td/tg"

	"github.com/iyear/tdl/core/storage"
	"github.com/iyear/tdl/core/util/tutil"
)

type ClickOptions struct {
	URL       string
	Chat      string
	LatestBot bool
	Inspect   bool
	Row       int
	Col       int
	Text      string
	Data      string
}

type buttonSelection struct {
	row     int
	col     int
	text    string
	button  tg.KeyboardButtonClass
	skipped bool
}

func Click(ctx context.Context, c *telegram.Client, kvd storage.Storage, opts ClickOptions) error {
	if err := opts.Validate(); err != nil {
		return err
	}

	manager := peers.Options{Storage: storage.NewPeers(kvd)}.Build(c.API())
	peer, msg, err := resolveTargetMessage(ctx, c, manager, opts)
	if err != nil {
		return err
	}

	markup, ok := msg.ReplyMarkup.(*tg.ReplyInlineMarkup)
	if !ok || markup == nil || len(markup.Rows) == 0 {
		return fmt.Errorf("message has no inline keyboard")
	}
	if opts.Inspect {
		printButtonStructure(msg.ID, markup.Rows)
		return nil
	}

	sel, warnings, err := selectButton(markup.Rows, opts)
	if err != nil {
		return err
	}

	for _, w := range warnings {
		fmt.Printf("WARN: %s\n", w)
	}

	fmt.Printf("Selected button: row=%d col=%d type=%s text=%q\n", sel.row, sel.col, buttonType(sel.button), sel.text)

	if sel.skipped {
		fmt.Printf("Skipped: button type %s is not clickable via messages.getBotCallbackAnswer\n", buttonType(sel.button))
		return nil
	}

	req, reqWarnings, err := buildBotCallbackRequest(peer.InputPeer(), msg.ID, sel.button, opts)
	if err != nil {
		return err
	}
	for _, w := range reqWarnings {
		fmt.Printf("WARN: %s\n", w)
	}

	if data, ok := req.GetData(); ok {
		fmt.Printf("Request: game=%v data_len=%d data=%q\n", req.GetGame(), len(data), string(data))
	} else {
		fmt.Printf("Request: game=%v data=<nil>\n", req.GetGame())
	}

	ans, err := c.API().MessagesGetBotCallbackAnswer(ctx, req)
	if err != nil {
		return err
	}

	fmt.Printf("Answer: alert=%v native_ui=%v has_url=%v cache_time=%d\n",
		ans.GetAlert(), ans.GetNativeUI(), ans.GetHasURL(), ans.GetCacheTime())
	if message, ok := ans.GetMessage(); ok {
		fmt.Printf("Answer message: %s\n", message)
	}
	if u, ok := ans.GetURL(); ok {
		fmt.Printf("Answer URL: %s\n", u)
	}

	return nil
}

func (opts ClickOptions) Validate() error {
	hasURL := strings.TrimSpace(opts.URL) != ""

	if opts.Inspect {
		if !opts.LatestBot {
			return fmt.Errorf("--inspect requires --latest-bot")
		}
		if strings.TrimSpace(opts.Chat) == "" {
			return fmt.Errorf("--inspect requires --chat")
		}
	}

	if opts.LatestBot {
		if hasURL {
			return fmt.Errorf("--url and --latest-bot cannot be used together")
		}
		if strings.TrimSpace(opts.Chat) == "" {
			return fmt.Errorf("--chat must be provided when --latest-bot is set")
		}
	} else {
		if !hasURL {
			return fmt.Errorf("either --url or --latest-bot must be provided")
		}
	}

	if !opts.Inspect {
		rowSet := opts.Row != 0
		colSet := opts.Col != 0
		if rowSet != colSet {
			return fmt.Errorf("--row and --col must be used together")
		}
		if rowSet {
			if opts.Row < 1 || opts.Col < 1 {
				return fmt.Errorf("--row and --col must be >= 1")
			}
			return nil
		}

		if strings.TrimSpace(opts.Text) == "" {
			return fmt.Errorf("either --row/--col or --text must be provided")
		}
	}
	return nil
}

func printButtonStructure(msgID int, rows []tg.KeyboardButtonRow) {
	fmt.Printf("Message %d inline keyboard:\n", msgID)
	for i, row := range rows {
		for j, btn := range row.Buttons {
			fmt.Printf("row=%d col=%d type=%s text=%q\n", i+1, j+1, buttonType(btn), buttonText(btn))
			if cb, ok := btn.(*tg.KeyboardButtonCallback); ok {
				fmt.Printf("  callback_data_base64=%s\n", base64.StdEncoding.EncodeToString(cb.Data))
				fmt.Printf("  callback_data_text=%q\n", string(cb.Data))
			}
		}
	}
}

func resolveTargetMessage(ctx context.Context, c *telegram.Client, manager *peers.Manager, opts ClickOptions) (peers.Peer, *tg.Message, error) {
	if !opts.LatestBot {
		peer, msgID, err := tutil.ParseMessageLink(ctx, manager, opts.URL)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to parse --url: %w", err)
		}

		msg, err := tutil.GetSingleMessage(ctx, c.API(), peer.InputPeer(), msgID)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to get message: %w", err)
		}
		return peer, msg, nil
	}

	peer, err := tutil.GetInputPeer(ctx, manager, opts.Chat)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get --chat peer: %w", err)
	}

	it := query.Messages(c.API()).GetHistory(peer.InputPeer()).BatchSize(50).Iter()
	for it.Next(ctx) {
		msg, ok := it.Value().Msg.(*tg.Message)
		if !ok {
			continue
		}
		if msg.Out {
			continue
		}
		return peer, msg, nil
	}
	if err = it.Err(); err != nil {
		return nil, nil, fmt.Errorf("failed to fetch latest bot message: %w", err)
	}

	return nil, nil, fmt.Errorf("no incoming message found in chat %q", opts.Chat)
}

func selectButton(rows []tg.KeyboardButtonRow, opts ClickOptions) (buttonSelection, []string, error) {
	rowSet := opts.Row != 0 && opts.Col != 0
	hasText := strings.TrimSpace(opts.Text) != ""
	warnings := make([]string, 0, 1)

	if rowSet {
		if hasText {
			warnings = append(warnings, "--row/--col are provided; --text is ignored")
		}
		if opts.Row > len(rows) {
			return buttonSelection{}, nil, fmt.Errorf("row index out of range: %d", opts.Row)
		}
		row := rows[opts.Row-1]
		if opts.Col > len(row.Buttons) {
			return buttonSelection{}, nil, fmt.Errorf("column index out of range: %d", opts.Col)
		}

		btn := row.Buttons[opts.Col-1]
		return buttonSelection{
			row:     opts.Row,
			col:     opts.Col,
			text:    buttonText(btn),
			button:  btn,
			skipped: !isClickableButton(btn),
		}, warnings, nil
	}

	for r, row := range rows {
		for c, btn := range row.Buttons {
			if buttonText(btn) != opts.Text {
				continue
			}

			if !isClickableButton(btn) {
				continue
			}

			return buttonSelection{
				row:    r + 1,
				col:    c + 1,
				text:   buttonText(btn),
				button: btn,
			}, warnings, nil
		}
	}

	for r, row := range rows {
		for c, btn := range row.Buttons {
			if buttonText(btn) != opts.Text {
				continue
			}
			return buttonSelection{
				row:    r + 1,
				col:    c + 1,
				text:   buttonText(btn),
				button: btn,
			}, warnings, fmt.Errorf("no clickable button matched text %q (all matched buttons are unsupported types)", opts.Text)
		}
	}

	return buttonSelection{}, warnings, fmt.Errorf("no button matched text %q", opts.Text)
}

func buildBotCallbackRequest(peer tg.InputPeerClass, msgID int, btn tg.KeyboardButtonClass, opts ClickOptions) (*tg.MessagesGetBotCallbackAnswerRequest, []string, error) {
	req := &tg.MessagesGetBotCallbackAnswerRequest{
		Peer:  peer,
		MsgID: msgID,
	}
	warnings := make([]string, 0, 1)

	switch b := btn.(type) {
	case *tg.KeyboardButtonCallback:
		data := b.Data
		if opts.Data != "" {
			data = []byte(opts.Data)
		}
		req.SetData(data)
	case *tg.KeyboardButtonGame:
		req.SetGame(true)
		if opts.Data != "" {
			warnings = append(warnings, "--data is ignored for game buttons")
		}
	default:
		return nil, nil, fmt.Errorf("button type %s is not supported", buttonType(btn))
	}

	return req, warnings, nil
}

func isClickableButton(btn tg.KeyboardButtonClass) bool {
	switch btn.(type) {
	case *tg.KeyboardButtonCallback, *tg.KeyboardButtonGame:
		return true
	default:
		return false
	}
}

func buttonType(btn tg.KeyboardButtonClass) string {
	return strings.TrimPrefix(fmt.Sprintf("%T", btn), "*tg.")
}

func buttonText(btn tg.KeyboardButtonClass) string {
	type textGetter interface {
		GetText() string
	}
	if t, ok := btn.(textGetter); ok {
		return t.GetText()
	}
	return ""
}
