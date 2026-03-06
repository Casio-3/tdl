package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/telegram/query"
	"github.com/gotd/td/tg"
	"gopkg.in/yaml.v3"

	"github.com/iyear/tdl/core/dcpool"
	"github.com/iyear/tdl/core/forwarder"
	"github.com/iyear/tdl/core/storage"
	"github.com/iyear/tdl/core/tclient"
	"github.com/iyear/tdl/core/util/tutil"
)

const (
	targetLatestInbound       = "latest_inbound_message"
	targetLatestInboundButton = "latest_inbound_button_message"
	actionModeLoop            = "loop"
	actionModeSequence        = "sequence"
	onNoMatchFail             = "fail"
	onNoMatchSkip             = "skip"
)

type ClickFlowOptions struct {
	Chat         string
	Flow         string
	Output       string
	ForwardTo    string
	MaxSteps     int
	Timeout      time.Duration
	PollInterval time.Duration
}

func (o ClickFlowOptions) Validate() error {
	if strings.TrimSpace(o.Chat) == "" {
		return fmt.Errorf("missing --chat")
	}
	if strings.TrimSpace(o.Flow) == "" {
		return fmt.Errorf("missing --flow")
	}
	if strings.TrimSpace(o.Output) == "" {
		return fmt.Errorf("missing --output")
	}
	if o.MaxSteps < 0 {
		return fmt.Errorf("--max-steps must be >= 0")
	}
	if o.Timeout < 0 {
		return fmt.Errorf("--timeout must be >= 0")
	}
	if o.PollInterval < 0 {
		return fmt.Errorf("--poll-interval must be >= 0")
	}
	return nil
}

type FlowConfig struct {
	Target         string             `yaml:"target"`
	Selectors      []FlowSelector     `yaml:"selectors"`
	Actions        FlowActions        `yaml:"actions"`
	StopConditions FlowStopConditions `yaml:"stop_conditions"`
	MediaScope     FlowMediaScope     `yaml:"media_scope"`
	Forward        FlowForward        `yaml:"forward"`
	BotProfile     string             `yaml:"bot_profile"`
	Runtime        FlowRuntime        `yaml:"runtime"`
}

type FlowForward struct {
	To   string `yaml:"to"`
	Mode string `yaml:"mode"`
	// Fallback controls behavior when restricted messages are detected.
	// none (default): return forward_restricted error.
	// clone: clone-forward by download/re-upload.
	Fallback string `yaml:"fallback"`
}

type FlowSelector struct {
	ID    string `yaml:"id"`
	Text  string `yaml:"text"`
	Regex string `yaml:"regex"`
	// Number matches the first number in button text, e.g. "· 1 ·", "10".
	Number int `yaml:"number"`
}

type FlowActions struct {
	Mode      string   `yaml:"mode"`
	Initial   []string `yaml:"initial"`
	Sequence  []string `yaml:"sequence"`
	Loop      []string `yaml:"loop"`
	OnNoMatch string   `yaml:"on_no_match"`
}

type FlowStopConditions struct {
	TextContains     []string `yaml:"text_contains"`
	TextRegex        []string `yaml:"text_regex"`
	AnyButtonPresent []string `yaml:"any_buttons_present"`
	AllButtonsAbsent []string `yaml:"all_buttons_absent"`
	IdleRounds       int      `yaml:"idle_rounds"`
}

type FlowMediaScope struct {
	Mode string `yaml:"mode"`
}

type FlowRuntime struct {
	MaxSteps     int    `yaml:"max_steps"`
	Timeout      string `yaml:"timeout"`
	PollInterval string `yaml:"poll_interval"`
}

type flowRuntime struct {
	maxSteps     int
	timeout      time.Duration
	pollInterval time.Duration
}

type flowError struct {
	category string
	err      error
}

func (e flowError) Error() string {
	return fmt.Sprintf("[%s] %v", e.category, e.err)
}

type flowSnapshot struct {
	messages            []*tg.Message
	latestInbound       *tg.Message
	latestInboundButton *tg.Message
}

type flowStepReport struct {
	Step             int      `json:"step"`
	Phase            string   `json:"phase"`
	SelectorID       string   `json:"selector_id"`
	MessageID        int      `json:"message_id"`
	ButtonRow        int      `json:"button_row"`
	ButtonCol        int      `json:"button_col"`
	ButtonText       string   `json:"button_text"`
	ButtonType       string   `json:"button_type"`
	ButtonsInMessage []string `json:"buttons_in_message,omitempty"`
	MediaTotal       int      `json:"media_total"`
	LatestInboundID  int      `json:"latest_inbound_id"`
	LatestText       string   `json:"latest_text"`
	AnswerMessage    string   `json:"answer_message,omitempty"`
	AnswerURL        string   `json:"answer_url,omitempty"`
}

type flowMediaEntry struct {
	ID   int    `json:"id"`
	Type string `json:"type"`
	Date int    `json:"date"`
}

type flowReport struct {
	Chat          string            `json:"chat"`
	BotProfile    string            `json:"bot_profile,omitempty"`
	FlowFile      string            `json:"flow_file"`
	StartedAt     time.Time         `json:"started_at"`
	FinishedAt    time.Time         `json:"finished_at"`
	StopReason    string            `json:"stop_reason"`
	ErrorCategory string            `json:"error_category,omitempty"`
	Error         string            `json:"error,omitempty"`
	Runtime       map[string]string `json:"runtime"`
	BaselineMaxID int               `json:"baseline_max_id"`
	Steps         []flowStepReport  `json:"steps"`
	Media         flowReportMedia   `json:"media"`
	Forward       flowReportForward `json:"forward"`
}

type compiledSelector struct {
	raw   FlowSelector
	regex *regexp.Regexp
}

type compiledForward struct {
	to       string
	mode     string
	fallback string
}

type selectedButton struct {
	selectorID string
	messageID  int
	row        int
	col        int
	button     tg.KeyboardButtonClass
}

func ClickFlow(ctx context.Context, c *telegram.Client, kvd storage.Storage, opts ClickFlowOptions) error {
	if err := opts.Validate(); err != nil {
		return err
	}

	cfg, err := loadFlowConfig(opts.Flow)
	if err != nil {
		return err
	}

	runtime, err := resolveRuntime(cfg.Runtime, opts)
	if err != nil {
		return err
	}
	fwd, err := resolveForward(cfg.Forward, opts)
	if err != nil {
		return err
	}

	selectorMap, err := compileSelectors(cfg.Selectors)
	if err != nil {
		return err
	}

	manager := peers.Options{Storage: storage.NewPeers(kvd)}.Build(c.API())
	peer, err := tutil.GetInputPeer(ctx, manager, opts.Chat)
	if err != nil {
		return fmt.Errorf("resolve chat: %w", err)
	}

	report := &flowReport{
		Chat:       opts.Chat,
		BotProfile: cfg.BotProfile,
		FlowFile:   opts.Flow,
		StartedAt:  time.Now(),
		Runtime: map[string]string{
			"max_steps":     fmt.Sprintf("%d", runtime.maxSteps),
			"timeout":       runtime.timeout.String(),
			"poll_interval": runtime.pollInterval.String(),
		},
	}
	report.Media.Types = map[string]int{}
	report.Forward = flowReportForward{
		Enabled:  fwd.to != "",
		To:       fwd.to,
		Mode:     fwd.mode,
		Fallback: fwd.fallback,
	}

	baseline, err := fetchSnapshot(ctx, c, peer)
	if err != nil {
		return writeFlowErrorReport(report, opts.Output, flowError{category: "snapshot", err: err})
	}
	baseMaxID := maxMessageID(baseline.messages)
	report.BaselineMaxID = baseMaxID
	mediaCollector := newMediaCollector(baseMaxID)

	stopReason, ferr := runFlow(ctx, c, peer, cfg, selectorMap, runtime, mediaCollector, report)
	if ferr.err != nil {
		return writeFlowErrorReport(report, opts.Output, ferr)
	}

	report.StopReason = stopReason
	report.FinishedAt = time.Now()
	report.Media = mediaCollector.report()

	if report.Forward.Enabled {
		toPeer, err := tutil.GetInputPeer(ctx, manager, report.Forward.To)
		if err != nil {
			return writeFlowErrorReport(report, opts.Output, flowError{
				category: "forward_resolve_peer",
				err:      err,
			})
		}

		ids := mediaCollector.forwardIDs(report.Forward.Mode)
		report.Forward.Attempted = len(ids)
		report.Forward.Plan = mediaCollector.forwardPlan(ids)
		report.Forward.RestrictedIDs = restrictedMessageIDs(report.Forward.Plan)
		printForwardPlan(report.Forward)
		if len(report.Forward.RestrictedIDs) > 0 {
			report.Forward.Hint = "some messages are protected (noforwards=true); set forward.fallback=clone to clone-forward them"
			if report.Forward.Fallback != "clone" {
				return writeFlowErrorReport(report, opts.Output, flowError{
					category: "forward_restricted",
					err: fmt.Errorf("CHAT_FORWARDS_RESTRICTED for ids=%v; these require clone mode",
						report.Forward.RestrictedIDs),
				})
			}
			msgs, err := mediaCollector.messagesByID(ids)
			if err != nil {
				return writeFlowErrorReport(report, opts.Output, flowError{
					category: "forward_clone_prepare",
					err:      err,
				})
			}
			forwardedIDs, ferr := cloneForwardMessages(ctx, c, peer, toPeer, msgs)
			report.Forward.Forwarded = len(forwardedIDs)
			report.Forward.IDs = forwardedIDs
			if ferr.err != nil {
				return writeFlowErrorReport(report, opts.Output, ferr)
			}
			goto reportDone
		}
		if len(ids) > 0 {
			forwardedIDs, ferr := forwardMessages(ctx, c, peer, toPeer, ids)
			report.Forward.Forwarded = len(forwardedIDs)
			report.Forward.IDs = forwardedIDs
			if ferr.err != nil {
				return writeFlowErrorReport(report, opts.Output, ferr)
			}
		}
	}

reportDone:
	if err = writeFlowReport(report, opts.Output); err != nil {
		return err
	}

	printFlowSummary(report, opts.Output)
	return nil
}

func runFlow(
	ctx context.Context,
	c *telegram.Client,
	peer peers.Peer,
	cfg *FlowConfig,
	selectors map[string]compiledSelector,
	runtime flowRuntime,
	collector *mediaCollector,
	report *flowReport,
) (string, flowError) {
	started := time.Now()
	step := 0
	idle := 0
	current, err := fetchSnapshot(ctx, c, peer)
	if err != nil {
		return "", flowError{category: "snapshot", err: err}
	}
	collector.consume(current.messages)

	if len(cfg.Actions.Initial) > 0 {
		sb, ok := findFirstMatchingButton(cfg.Actions.Initial, selectors, resolveClickableMessage(cfg.Target, current))
		if !ok {
			if cfg.Actions.OnNoMatch == onNoMatchSkip {
				idle++
			} else {
				return "", flowError{category: "selector_not_found", err: fmt.Errorf("no initial selector matched")}
			}
		} else {
			answerMsg, answerURL, ferr := clickSelected(ctx, c, peer, sb)
			if ferr.err != nil {
				return "", ferr
			}
			step++
			time.Sleep(runtime.pollInterval)
			next, err := fetchSnapshot(ctx, c, peer)
			if err != nil {
				return "", flowError{category: "snapshot", err: err}
			}
			current = next
			collector.consume(current.messages)
			report.Steps = append(report.Steps, makeStepReport(step, "initial", sb, current, collector, answerMsg, answerURL))
		}
	}

	mode := cfg.Actions.Mode
	if mode == "" {
		mode = actionModeLoop
	}

	switch mode {
	case actionModeSequence:
		for _, selectorID := range cfg.Actions.Sequence {
			if shouldTimeout(started, runtime.timeout) {
				return "timeout", flowError{}
			}
			if runtime.maxSteps > 0 && step >= runtime.maxSteps {
				return "max_steps", flowError{}
			}

			sb, ok := findFirstMatchingButton([]string{selectorID}, selectors, resolveClickableMessage(cfg.Target, current))
			if !ok {
				if cfg.Actions.OnNoMatch == onNoMatchSkip {
					idle++
					continue
				}
				return "", flowError{category: "selector_not_found", err: fmt.Errorf("selector %q not matched", selectorID)}
			}

			answerMsg, answerURL, ferr := clickSelected(ctx, c, peer, sb)
			if ferr.err != nil {
				return "", ferr
			}
			step++
			time.Sleep(runtime.pollInterval)
			next, err := fetchSnapshot(ctx, c, peer)
			if err != nil {
				return "", flowError{category: "snapshot", err: err}
			}
			current = next
			collector.consume(current.messages)
			report.Steps = append(report.Steps, makeStepReport(step, "sequence", sb, current, collector, answerMsg, answerURL))
		}
		return "sequence_completed", flowError{}

	case actionModeLoop:
		for {
			if shouldTimeout(started, runtime.timeout) {
				return "timeout", flowError{}
			}
			if runtime.maxSteps > 0 && step >= runtime.maxSteps {
				return "max_steps", flowError{}
			}

			if stopMatched(cfg.StopConditions, selectors, current, idle) {
				return "stop_conditions_matched", flowError{}
			}

			sb, ok := findFirstMatchingButton(cfg.Actions.Loop, selectors, resolveClickableMessage(cfg.Target, current))
			if !ok {
				if cfg.Actions.OnNoMatch == onNoMatchSkip {
					idle++
					time.Sleep(runtime.pollInterval)
					next, err := fetchSnapshot(ctx, c, peer)
					if err != nil {
						return "", flowError{category: "snapshot", err: err}
					}
					current = next
					collector.consume(current.messages)
					continue
				}
				return "", flowError{category: "selector_not_found", err: fmt.Errorf("no loop selector matched")}
			}

			answerMsg, answerURL, ferr := clickSelected(ctx, c, peer, sb)
			if ferr.err != nil {
				return "", ferr
			}
			step++
			idle = 0
			time.Sleep(runtime.pollInterval)
			next, err := fetchSnapshot(ctx, c, peer)
			if err != nil {
				return "", flowError{category: "snapshot", err: err}
			}
			current = next
			collector.consume(current.messages)
			report.Steps = append(report.Steps, makeStepReport(step, "loop", sb, current, collector, answerMsg, answerURL))
		}
	default:
		return "", flowError{category: "config", err: fmt.Errorf("unknown actions.mode: %s", mode)}
	}
}

func resolveRuntime(cfg FlowRuntime, opts ClickFlowOptions) (flowRuntime, error) {
	rt := flowRuntime{
		maxSteps:     50,
		timeout:      2 * time.Minute,
		pollInterval: time.Second,
	}
	if cfg.MaxSteps > 0 {
		rt.maxSteps = cfg.MaxSteps
	}
	if cfg.Timeout != "" {
		d, err := time.ParseDuration(cfg.Timeout)
		if err != nil {
			return rt, fmt.Errorf("parse runtime.timeout: %w", err)
		}
		rt.timeout = d
	}
	if cfg.PollInterval != "" {
		d, err := time.ParseDuration(cfg.PollInterval)
		if err != nil {
			return rt, fmt.Errorf("parse runtime.poll_interval: %w", err)
		}
		rt.pollInterval = d
	}

	if opts.MaxSteps > 0 {
		rt.maxSteps = opts.MaxSteps
	}
	if opts.Timeout > 0 {
		rt.timeout = opts.Timeout
	}
	if opts.PollInterval > 0 {
		rt.pollInterval = opts.PollInterval
	}
	return rt, nil
}

func loadFlowConfig(path string) (*FlowConfig, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read flow file: %w", err)
	}

	var cfg FlowConfig
	if err = yaml.Unmarshal(b, &cfg); err != nil {
		return nil, fmt.Errorf("parse flow yaml: %w", err)
	}
	if err = cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *FlowConfig) Validate() error {
	switch c.Target {
	case targetLatestInbound, targetLatestInboundButton:
	default:
		return fmt.Errorf("invalid target: %s", c.Target)
	}
	if len(c.Selectors) == 0 {
		return fmt.Errorf("selectors cannot be empty")
	}
	if len(c.Actions.Initial) == 0 && len(c.Actions.Sequence) == 0 && len(c.Actions.Loop) == 0 {
		return fmt.Errorf("actions cannot be empty")
	}
	if c.Actions.Mode == "" {
		c.Actions.Mode = actionModeLoop
	}
	if c.Actions.OnNoMatch == "" {
		c.Actions.OnNoMatch = onNoMatchFail
	}
	if c.Actions.OnNoMatch != onNoMatchFail && c.Actions.OnNoMatch != onNoMatchSkip {
		return fmt.Errorf("invalid actions.on_no_match: %s", c.Actions.OnNoMatch)
	}
	if c.Actions.Mode != actionModeLoop && c.Actions.Mode != actionModeSequence {
		return fmt.Errorf("invalid actions.mode: %s", c.Actions.Mode)
	}
	if c.Actions.Mode == actionModeSequence && len(c.Actions.Sequence) == 0 {
		return fmt.Errorf("actions.sequence required for sequence mode")
	}
	if c.Actions.Mode == actionModeLoop && len(c.Actions.Loop) == 0 {
		return fmt.Errorf("actions.loop required for loop mode")
	}
	if c.MediaScope.Mode == "" {
		c.MediaScope.Mode = "since_start"
	}
	if c.MediaScope.Mode != "since_start" {
		return fmt.Errorf("invalid media_scope.mode: %s", c.MediaScope.Mode)
	}
	if c.Forward.Mode == "" {
		c.Forward.Mode = "media_only"
	}
	if c.Forward.Mode != "media_only" && c.Forward.Mode != "all_messages" {
		return fmt.Errorf("invalid forward.mode: %s", c.Forward.Mode)
	}
	if c.Forward.Fallback == "" {
		c.Forward.Fallback = "none"
	}
	if c.Forward.Fallback != "none" && c.Forward.Fallback != "clone" {
		return fmt.Errorf("invalid forward.fallback: %s", c.Forward.Fallback)
	}
	if c.StopConditions.IdleRounds < 0 {
		return fmt.Errorf("stop_conditions.idle_rounds must be >= 0")
	}
	return nil
}

func resolveForward(cfg FlowForward, opts ClickFlowOptions) (compiledForward, error) {
	out := compiledForward{
		to:       strings.TrimSpace(cfg.To),
		mode:     cfg.Mode,
		fallback: cfg.Fallback,
	}
	if out.mode == "" {
		out.mode = "media_only"
	}
	if out.fallback == "" {
		out.fallback = "none"
	}
	if strings.TrimSpace(opts.ForwardTo) != "" {
		out.to = strings.TrimSpace(opts.ForwardTo)
	}
	if out.mode != "media_only" && out.mode != "all_messages" {
		return out, fmt.Errorf("invalid forward mode: %s", out.mode)
	}
	if out.fallback != "none" && out.fallback != "clone" {
		return out, fmt.Errorf("invalid forward fallback: %s", out.fallback)
	}
	return out, nil
}

func compileSelectors(in []FlowSelector) (map[string]compiledSelector, error) {
	out := make(map[string]compiledSelector, len(in))
	for _, s := range in {
		if s.ID == "" {
			return nil, fmt.Errorf("selector id cannot be empty")
		}
		if _, exists := out[s.ID]; exists {
			return nil, fmt.Errorf("duplicate selector id: %s", s.ID)
		}
		modeCnt := 0
		if s.Text != "" {
			modeCnt++
		}
		if s.Regex != "" {
			modeCnt++
		}
		if s.Number > 0 {
			modeCnt++
		}
		if modeCnt != 1 {
			return nil, fmt.Errorf("selector %q must set exactly one of text/regex/number", s.ID)
		}
		cs := compiledSelector{raw: s}
		if s.Regex != "" {
			r, err := regexp.Compile(s.Regex)
			if err != nil {
				return nil, fmt.Errorf("selector %q regex: %w", s.ID, err)
			}
			cs.regex = r
		}
		out[s.ID] = cs
	}
	return out, nil
}

func fetchSnapshot(ctx context.Context, c *telegram.Client, peer peers.Peer) (flowSnapshot, error) {
	it := query.Messages(c.API()).GetHistory(peer.InputPeer()).BatchSize(120).Iter()

	msgs := make([]*tg.Message, 0, 120)
	for it.Next(ctx) {
		msg, ok := it.Value().Msg.(*tg.Message)
		if !ok || msg.Out {
			continue
		}
		msgs = append(msgs, msg)
	}
	if err := it.Err(); err != nil {
		return flowSnapshot{}, err
	}
	if len(msgs) == 0 {
		return flowSnapshot{}, fmt.Errorf("no inbound messages found")
	}

	s := flowSnapshot{
		messages:      msgs,
		latestInbound: msgs[0],
	}
	for _, m := range msgs {
		if _, ok := m.ReplyMarkup.(*tg.ReplyInlineMarkup); ok {
			s.latestInboundButton = m
			break
		}
	}
	return s, nil
}

func resolveClickableMessage(target string, snap flowSnapshot) *tg.Message {
	if target == targetLatestInboundButton {
		return snap.latestInboundButton
	}
	if snap.latestInbound != nil {
		if _, ok := snap.latestInbound.ReplyMarkup.(*tg.ReplyInlineMarkup); ok {
			return snap.latestInbound
		}
	}
	return snap.latestInboundButton
}

func findFirstMatchingButton(selectorIDs []string, selectors map[string]compiledSelector, msg *tg.Message) (selectedButton, bool) {
	if msg == nil {
		return selectedButton{}, false
	}
	markup, ok := msg.ReplyMarkup.(*tg.ReplyInlineMarkup)
	if !ok || markup == nil {
		return selectedButton{}, false
	}
	for _, sid := range selectorIDs {
		sel, ok := selectors[sid]
		if !ok {
			continue
		}
		for r, row := range markup.Rows {
			for c, btn := range row.Buttons {
				if matchSelector(sel, btn) {
					return selectedButton{
						selectorID: sid,
						messageID:  msg.ID,
						row:        r + 1,
						col:        c + 1,
						button:     btn,
					}, true
				}
			}
		}
	}
	return selectedButton{}, false
}

var firstDigitsRe = regexp.MustCompile(`\d+`)

func matchSelector(sel compiledSelector, btn tg.KeyboardButtonClass) bool {
	text := buttonText(btn)
	switch {
	case sel.raw.Text != "":
		return text == sel.raw.Text
	case sel.raw.Number > 0:
		n := firstDigitsRe.FindString(text)
		if n == "" {
			return false
		}
		return n == fmt.Sprintf("%d", sel.raw.Number)
	case sel.regex != nil:
		return sel.regex.MatchString(text)
	default:
		return false
	}
}

func stopMatched(stop FlowStopConditions, selectors map[string]compiledSelector, snap flowSnapshot, idle int) bool {
	latestText := ""
	if snap.latestInbound != nil {
		latestText = snap.latestInbound.Message
	}

	containsOK := true
	if len(stop.TextContains) > 0 {
		containsOK = false
		for _, s := range stop.TextContains {
			if strings.Contains(latestText, s) {
				containsOK = true
				break
			}
		}
	}

	regexOK := true
	if len(stop.TextRegex) > 0 {
		regexOK = false
		for _, p := range stop.TextRegex {
			re, err := regexp.Compile(p)
			if err != nil {
				continue
			}
			if re.MatchString(latestText) {
				regexOK = true
				break
			}
		}
	}

	anyButtonPresentOK := true
	if len(stop.AnyButtonPresent) > 0 {
		anyButtonPresentOK = false
		if msg := resolveClickableMessage(targetLatestInboundButton, snap); msg != nil {
			for _, sid := range stop.AnyButtonPresent {
				if _, ok := findFirstMatchingButton([]string{sid}, selectors, msg); ok {
					anyButtonPresentOK = true
					break
				}
			}
		}
	}

	allButtonsAbsentOK := true
	if len(stop.AllButtonsAbsent) > 0 {
		allButtonsAbsentOK = true
		if msg := resolveClickableMessage(targetLatestInboundButton, snap); msg != nil {
			for _, sid := range stop.AllButtonsAbsent {
				if _, ok := findFirstMatchingButton([]string{sid}, selectors, msg); ok {
					allButtonsAbsentOK = false
					break
				}
			}
		}
	}

	idleOK := true
	if stop.IdleRounds > 0 {
		idleOK = idle >= stop.IdleRounds
	}

	return containsOK && regexOK && anyButtonPresentOK && allButtonsAbsentOK && idleOK
}

func clickSelected(ctx context.Context, c *telegram.Client, peer peers.Peer, sb selectedButton) (string, string, flowError) {
	req, _, err := buildBotCallbackRequest(peer.InputPeer(), sb.messageID, sb.button, ClickOptions{})
	if err != nil {
		return "", "", flowError{category: "request_build", err: err}
	}
	ans, err := c.API().MessagesGetBotCallbackAnswer(ctx, req)
	if err != nil {
		return "", "", flowError{category: "rpc", err: err}
	}
	msg, _ := ans.GetMessage()
	url, _ := ans.GetURL()
	return msg, url, flowError{}
}

func makeStepReport(step int, phase string, sb selectedButton, snap flowSnapshot, collector *mediaCollector, answerMsg, answerURL string) flowStepReport {
	msg := resolveClickableMessage(targetLatestInboundButton, snap)
	messageID := sb.messageID
	buttons := []string{}
	if msg != nil {
		markup, _ := msg.ReplyMarkup.(*tg.ReplyInlineMarkup)
		if markup != nil {
			for _, row := range markup.Rows {
				for _, b := range row.Buttons {
					buttons = append(buttons, buttonText(b))
				}
			}
		}
	}
	latestID := 0
	latestText := ""
	if snap.latestInbound != nil {
		latestID = snap.latestInbound.ID
		latestText = strings.ReplaceAll(snap.latestInbound.Message, "\n", " ")
	}
	return flowStepReport{
		Step:             step,
		Phase:            phase,
		SelectorID:       sb.selectorID,
		MessageID:        messageID,
		ButtonRow:        sb.row,
		ButtonCol:        sb.col,
		ButtonText:       buttonText(sb.button),
		ButtonType:       buttonType(sb.button),
		ButtonsInMessage: buttons,
		MediaTotal:       collector.total(),
		LatestInboundID:  latestID,
		LatestText:       latestText,
		AnswerMessage:    answerMsg,
		AnswerURL:        answerURL,
	}
}

func shouldTimeout(start time.Time, timeout time.Duration) bool {
	if timeout <= 0 {
		return false
	}
	return time.Since(start) >= timeout
}

type mediaCollector struct {
	baseMaxID int
	mediaData map[int]flowMediaEntry
	allIDs    map[int]struct{}
	allData   map[int]flowForwardPlanEntry
	allMsg    map[int]*tg.Message
}

func newMediaCollector(baseMaxID int) *mediaCollector {
	return &mediaCollector{
		baseMaxID: baseMaxID,
		mediaData: map[int]flowMediaEntry{},
		allIDs:    map[int]struct{}{},
		allData:   map[int]flowForwardPlanEntry{},
		allMsg:    map[int]*tg.Message{},
	}
}

func (m *mediaCollector) consume(messages []*tg.Message) {
	for _, msg := range messages {
		if msg.ID <= m.baseMaxID || msg.Out {
			continue
		}
		m.allIDs[msg.ID] = struct{}{}
		if _, ok := m.allMsg[msg.ID]; !ok {
			m.allMsg[msg.ID] = msg
		}
		plan := flowForwardPlanEntry{
			ID:         msg.ID,
			Type:       "text",
			Noforwards: msg.Noforwards,
		}
		if msg.Media == nil {
			m.allData[msg.ID] = plan
			continue
		}
		if _, ok := m.mediaData[msg.ID]; ok {
			continue
		}
		typ := mediaType(msg.Media)
		m.mediaData[msg.ID] = flowMediaEntry{
			ID:   msg.ID,
			Date: msg.Date,
			Type: typ,
		}
		plan.Type = typ
		m.allData[msg.ID] = plan
	}
}

func (m *mediaCollector) total() int {
	return len(m.mediaData)
}

func (m *mediaCollector) report() flowReportMedia {
	ids := make([]int, 0, len(m.mediaData))
	entries := make([]flowMediaEntry, 0, len(m.mediaData))
	types := map[string]int{}
	firstID, lastID := 0, 0
	firstDate, lastDate := 0, 0

	for id, entry := range m.mediaData {
		ids = append(ids, id)
		entries = append(entries, entry)
		types[entry.Type]++
		if firstID == 0 || id < firstID {
			firstID = id
		}
		if id > lastID {
			lastID = id
		}
		if firstDate == 0 || entry.Date < firstDate {
			firstDate = entry.Date
		}
		if entry.Date > lastDate {
			lastDate = entry.Date
		}
	}

	return flowReportMedia{
		Total:     len(entries),
		Types:     types,
		IDs:       ids,
		FirstID:   firstID,
		LastID:    lastID,
		FirstDate: firstDate,
		LastDate:  lastDate,
		Entries:   entries,
	}
}

func (m *mediaCollector) forwardIDs(mode string) []int {
	switch mode {
	case "all_messages":
		out := make([]int, 0, len(m.allIDs))
		for id := range m.allIDs {
			out = append(out, id)
		}
		sort.Ints(out)
		return out
	default:
		out := make([]int, 0, len(m.mediaData))
		for id := range m.mediaData {
			out = append(out, id)
		}
		sort.Ints(out)
		return out
	}
}

func (m *mediaCollector) forwardPlan(ids []int) []flowForwardPlanEntry {
	out := make([]flowForwardPlanEntry, 0, len(ids))
	for _, id := range ids {
		if p, ok := m.allData[id]; ok {
			out = append(out, p)
			continue
		}
		out = append(out, flowForwardPlanEntry{
			ID:   id,
			Type: "unknown",
		})
	}
	return out
}

func (m *mediaCollector) messagesByID(ids []int) ([]*tg.Message, error) {
	out := make([]*tg.Message, 0, len(ids))
	for _, id := range ids {
		msg, ok := m.allMsg[id]
		if !ok {
			return nil, fmt.Errorf("message %d not found in collector", id)
		}
		out = append(out, msg)
	}
	return out, nil
}

func restrictedMessageIDs(plan []flowForwardPlanEntry) []int {
	out := make([]int, 0, len(plan))
	for _, p := range plan {
		if p.Noforwards {
			out = append(out, p.ID)
		}
	}
	return out
}

func printForwardPlan(f flowReportForward) {
	if !f.Enabled {
		return
	}
	fmt.Printf("Forward plan: mode=%s fallback=%s to=%s candidates=%d\n", f.Mode, f.Fallback, f.To, len(f.Plan))
	if len(f.Plan) == 0 {
		return
	}
	ids := make([]int, 0, len(f.Plan))
	for _, p := range f.Plan {
		ids = append(ids, p.ID)
	}
	fmt.Printf("Forward candidate IDs: %v\n", ids)
	if len(f.RestrictedIDs) > 0 {
		fmt.Printf("Forward restricted IDs (require clone): %v\n", f.RestrictedIDs)
	}
}

type flowReportMedia struct {
	Total     int              `json:"total"`
	Types     map[string]int   `json:"types"`
	IDs       []int            `json:"ids"`
	FirstID   int              `json:"first_id"`
	LastID    int              `json:"last_id"`
	FirstDate int              `json:"first_date"`
	LastDate  int              `json:"last_date"`
	Entries   []flowMediaEntry `json:"entries"`
}

type flowReportForward struct {
	Enabled       bool                   `json:"enabled"`
	To            string                 `json:"to,omitempty"`
	Mode          string                 `json:"mode,omitempty"`
	Fallback      string                 `json:"fallback,omitempty"`
	Attempted     int                    `json:"attempted"`
	Forwarded     int                    `json:"forwarded"`
	IDs           []int                  `json:"ids,omitempty"`
	Plan          []flowForwardPlanEntry `json:"plan,omitempty"`
	RestrictedIDs []int                  `json:"restricted_ids,omitempty"`
	Hint          string                 `json:"hint,omitempty"`
}

type flowForwardPlanEntry struct {
	ID         int    `json:"id"`
	Type       string `json:"type"`
	Noforwards bool   `json:"noforwards"`
}

func mediaType(m tg.MessageMediaClass) string {
	switch m.(type) {
	case *tg.MessageMediaPhoto:
		return "photo"
	case *tg.MessageMediaDocument:
		return "document"
	default:
		return "other"
	}
}

func forwardMessages(ctx context.Context, c *telegram.Client, from peers.Peer, to peers.Peer, ids []int) ([]int, flowError) {
	if len(ids) == 0 {
		return nil, flowError{}
	}

	const batchSize = 100
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	forwarded := make([]int, 0, len(ids))
	for start := 0; start < len(ids); start += batchSize {
		end := start + batchSize
		if end > len(ids) {
			end = len(ids)
		}
		batch := ids[start:end]
		randomIDs := make([]int64, len(batch))
		for i := range batch {
			randomIDs[i] = r.Int63()
		}

		req := &tg.MessagesForwardMessagesRequest{
			Silent:            false,
			Background:        false,
			WithMyScore:       false,
			DropAuthor:        false,
			DropMediaCaptions: false,
			Noforwards:        false,
			FromPeer:          from.InputPeer(),
			ID:                batch,
			RandomID:          randomIDs,
			ToPeer:            to.InputPeer(),
			TopMsgID:          0,
			ScheduleDate:      0,
			SendAs:            nil,
		}
		req.SetFlags()
		if _, err := c.API().MessagesForwardMessages(ctx, req); err != nil {
			return forwarded, flowError{category: "forward_rpc", err: err}
		}
		forwarded = append(forwarded, batch...)
	}
	return forwarded, flowError{}
}

type cloneIter struct {
	idx   int
	elems []forwarder.Elem
}

func (c *cloneIter) Next(_ context.Context) bool {
	if c.idx >= len(c.elems) {
		return false
	}
	c.idx++
	return true
}

func (c *cloneIter) Value() forwarder.Elem { return c.elems[c.idx-1] }
func (c *cloneIter) Err() error            { return nil }

type cloneElem struct {
	from peers.Peer
	to   peers.Peer
	msg  *tg.Message
}

func (e cloneElem) Mode() forwarder.Mode { return forwarder.ModeClone }
func (e cloneElem) From() peers.Peer     { return e.from }
func (e cloneElem) Msg() *tg.Message     { return e.msg }
func (e cloneElem) To() peers.Peer       { return e.to }
func (e cloneElem) Thread() int          { return 0 }
func (e cloneElem) AsSilent() bool       { return false }
func (e cloneElem) AsDryRun() bool       { return false }
func (e cloneElem) AsGrouped() bool      { return false }

type cloneProgress struct {
	errs []error
}

func (p *cloneProgress) OnAdd(_ forwarder.Elem) {}

func (p *cloneProgress) OnClone(_ forwarder.Elem, _ forwarder.ProgressState) {}

func (p *cloneProgress) OnDone(_ forwarder.Elem, err error) {
	if err != nil {
		p.errs = append(p.errs, err)
	}
}

func cloneForwardMessages(
	ctx context.Context,
	c *telegram.Client,
	from peers.Peer,
	to peers.Peer,
	msgs []*tg.Message,
) ([]int, flowError) {
	if len(msgs) == 0 {
		return nil, flowError{}
	}

	ids := make([]int, 0, len(msgs))
	elems := make([]forwarder.Elem, 0, len(msgs))
	for _, m := range msgs {
		ids = append(ids, m.ID)
		elems = append(elems, cloneElem{
			from: from,
			to:   to,
			msg:  m,
		})
	}

	pool := dcpool.NewPool(c, 1, tclient.NewDefaultMiddlewares(ctx, 0)...)
	defer func() { _ = pool.Close() }()

	progress := &cloneProgress{}
	fw := forwarder.New(forwarder.Options{
		Pool:     pool,
		Threads:  4,
		Iter:     &cloneIter{elems: elems},
		Progress: progress,
	})
	if err := fw.Forward(ctx); err != nil {
		return nil, flowError{category: "forward_clone", err: err}
	}
	if len(progress.errs) > 0 {
		return nil, flowError{category: "forward_clone", err: progress.errs[0]}
	}
	return ids, flowError{}
}

func maxMessageID(messages []*tg.Message) int {
	maxID := 0
	for _, m := range messages {
		if m.ID > maxID {
			maxID = m.ID
		}
	}
	return maxID
}

func writeFlowReport(r *flowReport, output string) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal flow report: %w", err)
	}
	if err = os.WriteFile(output, b, 0o644); err != nil {
		return fmt.Errorf("write flow report: %w", err)
	}
	return nil
}

func writeFlowErrorReport(r *flowReport, output string, ferr flowError) error {
	r.StopReason = "error"
	r.ErrorCategory = ferr.category
	r.Error = ferr.err.Error()
	r.FinishedAt = time.Now()
	r.Media = flowReportMedia{Types: map[string]int{}}
	if err := writeFlowReport(r, output); err != nil {
		return err
	}
	return ferr
}

func printFlowSummary(r *flowReport, output string) {
	fmt.Printf("Flow done: stop=%s steps=%d media_total=%d output=%s\n",
		r.StopReason, len(r.Steps), r.Media.Total, output)
	if r.ErrorCategory != "" {
		fmt.Printf("Flow error: category=%s error=%s\n", r.ErrorCategory, r.Error)
	}
	fmt.Printf("Media types: %v\n", r.Media.Types)
	if r.Forward.Enabled {
		fmt.Printf("Forward: to=%s mode=%s fallback=%s attempted=%d forwarded=%d\n",
			r.Forward.To, r.Forward.Mode, r.Forward.Fallback, r.Forward.Attempted, r.Forward.Forwarded)
	}
}
