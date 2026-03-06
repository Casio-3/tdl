---
title: "Click Flow"
weight: 40
---

# Click Flow

Run multi-step bot button interactions with a YAML flow file.

Use this when a bot keeps updating messages/buttons and you want to automate until stop conditions are met.

## Quick Start

{{< command >}}
tdl chat click flow \
  -c BOT_CHAT \
  --flow flow.yaml \
  -o flow-report.json \
  --forward-to 3786555826
{{< /command >}}

Output:
- Terminal summary: stop reason, steps, media totals
- JSON report file: full step logs, matched buttons, media details

## Flow File

Minimal structure:

```yaml
target: latest_inbound_button_message

selectors:
  - id: open
    text: "10"
  - id: next_batch
    text: "➡️ 点击查看下一组"

actions:
  mode: loop # loop | sequence
  initial: [open]
  loop: [next_batch]
  on_no_match: fail # fail | skip

stop_conditions:
  text_contains: ["文件夹内容发送完成", "发送完成"]
  all_buttons_absent: [next_batch]
  idle_rounds: 1

media_scope:
  mode: since_start

runtime:
  max_steps: 30
  timeout: 3m
  poll_interval: 1s

forward:
  to: "3786555826"
  mode: media_only # media_only | all_messages
```

## Selector Types

Each selector must set exactly one matching mode:

- `text`: exact button text
- `regex`: regex on button text
- `number`: first number in button text (works for `· 1 ·`, `2`, `10`, etc.)

## Common Patterns

### Batch Bot (next-group button)

- `actions.initial`: open folder/item (`10`, `📋 查看内容`)
- `actions.loop`: try `➡️ 点击查看下一组`, then fallback `➡️ 下一页`
- `stop_conditions`: completion text + next-button absent

### Paging Bot (page 1..N)

Use `mode: sequence`:

```yaml
actions:
  mode: sequence
  sequence: [p1, p2, p3, p4, p5, p6, p7]
  on_no_match: fail
```

and map selectors using `number`.

## CLI Overrides

You can override runtime values from CLI:

{{< command >}}
tdl chat click flow -c BOT_CHAT --flow flow.yaml \
  --max-steps 50 \
  --timeout 5m \
  --poll-interval 800ms
{{< /command >}}

## Notes

- Default behavior is fail-fast (`on_no_match: fail`).
- Media statistics are incremental from flow start (`since_start`) and deduplicated by message ID.
- `target`:
  - `latest_inbound_button_message`
  - `latest_inbound_message`
- `forward`:
  - `media_only`: forward only media messages captured during this flow
  - `all_messages`: forward all inbound messages captured during this flow
