/**
 * FeatureCard is the shell every feature's settings section uses.
 *
 * A feature's switch and its parameters belong together: an operator turning
 * "进群自动绑定" on needs to see, in the same block, what it will do and what it
 * costs. Putting the switch in one list and the interval in another is what made
 * the old page hard to read — the relationship was only implied by ordering.
 *
 * The switch lives in the card's header (via `actions`) rather than beside each
 * field, so "this whole module is off" is one glance.
 */
import { useId, type ReactNode } from 'react'
import { Badge, Card, Switch } from '../ui'

export function FeatureCard({
  title,
  description,
  enabled,
  onToggle,
  toggleLabel,
  disabled,
  badge,
  children,
}: {
  title: string
  description?: string
  /** When omitted the card has no master switch. */
  enabled?: boolean
  onToggle?: (v: boolean) => void
  toggleLabel?: string
  disabled?: boolean
  badge?: ReactNode
  children?: ReactNode
}) {
  const off = enabled === false
  // The switch's accessible name is the feature, not its state: six switches all
  // announced as "已启用" give a screen-reader user no way to tell which feature
  // they are on. The visible state word stays as the adjacent label.
  const stateWord = enabled ? (toggleLabel ?? '已启用') : '已停用'
  return (
    <Card
      title={
        <span className="inline-flex items-center gap-2">
          {title}
          {off && <Badge>已停用</Badge>}
          {badge}
        </span>
      }
      description={description}
      actions={
        onToggle ? (
          <Switch
            checked={!!enabled}
            onChange={onToggle}
            label={stateWord}
            ariaLabel={`${title}：${stateWord}`}
            disabled={disabled}
          />
        ) : null
      }
    >
      {/*
        The body is dimmed rather than unmounted while the feature is off: the
        operator can still read (and edit) the parameters before switching it
        back on, and nothing jumps around. Inputs stay enabled for the same
        reason — the switch alone decides whether the feature runs.
      */}
      <div className={off ? 'space-y-4 opacity-60' : 'space-y-4'}>{children}</div>
    </Card>
  )
}

/**
 * CommandList is the editor for a command pattern list.
 *
 * One pattern per line is the format, so the control is a textarea and the hint
 * spells the syntax out. The literal/regex distinction is invisible otherwise,
 * and an operator who types `/签到` expecting a literal would get a regular
 * expression that also matches 我要签到.
 */
export function CommandList({
  value,
  onChange,
  hint,
  invalid,
}: {
  value: string
  onChange: (v: string) => void
  hint?: string
  invalid?: boolean
}) {
  const uid = useId()
  return (
    <div className="space-y-1.5">
      <textarea
        value={value}
        rows={4}
        spellCheck={false}
        aria-label="指令列表，一行一个"
        aria-invalid={invalid || undefined}
        aria-describedby={uid}
        onChange={(e) => onChange(e.target.value)}
        className={`block w-full rounded-control border bg-background px-2.5 py-1.5 font-mono text-xs leading-5 text-foreground transition-colors placeholder:text-muted-foreground ${
          invalid ? 'border-destructive' : 'border-input hover:border-ring/60'
        } focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[var(--ring)]`}
      />
      <p id={uid} className="text-[11px] leading-4 text-muted-foreground">
        {hint ?? '一行一个指令。字面量需整条消息完全匹配（前后空格忽略）；/表达式/ 是正则，未加 ^$ 时在消息里任意位置命中即可，例如 /^签到\\s*\\d*$/；/表达式/i 表示忽略大小写。'}
      </p>
    </div>
  )
}
