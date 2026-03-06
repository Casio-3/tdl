---
title: "按钮流程自动化"
weight: 40
---

# 按钮流程自动化

通过 YAML 流程文件执行多轮机器人按钮交互。

适用于机器人不断更新消息/按钮，需要持续点击直到满足结束条件的场景。

## 快速开始

{{< command >}}
tdl chat click flow \
  -c BOT_CHAT \
  --flow flow.yaml \
  -o flow-report.json \
  --forward-to 3786555826
{{< /command >}}

输出：
- 终端摘要：停止原因、步数、媒体总数
- JSON 报告：完整步骤日志、命中按钮、媒体明细

## 流程文件

最小结构：

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
  fallback: none # none | clone
```

## 选择器类型

每个 selector 必须且只能设置一种匹配方式：

- `text`：按钮文本精确匹配
- `regex`：按钮文本正则匹配
- `number`：匹配按钮文本里的第一个数字（如 `· 1 ·`、`2`、`10`）

## 常见模式

### 分批 bot（下一组）

- `actions.initial`：先打开目标（如 `10`、`📋 查看内容`）
- `actions.loop`：先尝试 `➡️ 点击查看下一组`，再尝试 `➡️ 下一页`
- `stop_conditions`：命中完成文案 + 下一组按钮不存在

### 分页 bot（1..N 页）

使用 `mode: sequence`：

```yaml
actions:
  mode: sequence
  sequence: [p1, p2, p3, p4, p5, p6, p7]
  on_no_match: fail
```

并用 `number` 定义页码按钮。

## CLI 覆盖

可在命令行覆盖运行时参数：

{{< command >}}
tdl chat click flow -c BOT_CHAT --flow flow.yaml \
  --max-steps 50 \
  --timeout 5m \
  --poll-interval 800ms
{{< /command >}}

## 说明

- 默认快速失败（`on_no_match: fail`）。
- 媒体统计按本次流程启动后增量计算（`since_start`），并按消息 ID 去重。
- `target` 可选：
  - `latest_inbound_button_message`
  - `latest_inbound_message`
- `forward` 可选：
  - `media_only`：仅转发本次流程中采集到的媒体消息
  - `all_messages`：转发本次流程中采集到的所有入站消息
  - `fallback`：
    - `none`（默认）：若命中受保护消息（`noforwards=true`），直接返回 `forward_restricted`
    - `clone`：自动走 clone 转发（下载再发送），可绕过官方转发限制但速度更慢
  - 转发前会预检查受保护消息，并在 JSON 报告里输出候选 ID 与受限 ID。
