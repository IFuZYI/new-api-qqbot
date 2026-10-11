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
        Body is dimmed rather than unmounted while the feature is off: the
        operator can still read (and edit) the parameters before switching it
        back on, and nothing jumps around. Inputs stay enabled for the same
        reason — the switch alone decides whether the feature runs.

        The dimming is a *surface* wash, not a whole-block opacity. `opacity-60`
        multiplies the alpha of every descendant, including text, and the
        panel's caption grey then measured 2.63:1 on a white card (3.56:1 in
        dark) — the hint lines that explain what a switched-off feature does
        were the least readable text on the page, which is exactly backwards. A
        `muted` tint over the body reads as "this is off" without touching a
        single glyph's contrast.

        The wash carries no padding. An earlier `p-3` version moved the inputs
        12px right and grew the card 24px the moment the switch flipped
        (measured 227px → 251px), so turning a feature off shoved the rest of
        the page down — the opposite of "nothing jumps around". The band's box
        is the card body's own content box, so both states are geometrically
        identical.

        `bg-muted` and not `bg-muted/50`: half-strength muted composites to
        rgb(250,250,250) on a white card, 1.044:1 — below the 1.10:1 the
        `--table-header` comment calls the smallest visible step in light mode,
        and close to the 1.035:1 it calls invisible. The off state would have
        had no surface signal at all. Full muted measures 1.09:1 light / 1.072:1
        dark, the same surface the muted badge uses.

        A card with nothing in its body (进群自动绑定 has only a switch) renders
        no body at all: an empty `p-4` measured 32px of blank card under a 45px
        header, which is the "why is there a hole here" the layout audit found.
        The state it would have shown is already in the header badge.
      */}
      {children && <div className={off ? 'space-y-4 rounded-panel bg-muted' : 'space-y-4'}>{children}</div>}
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
      <p id={uid} className="text-2xs text-muted-foreground">
        {hint ?? '一行一个指令。字面量需整条消息完全匹配（前后空格忽略）；/表达式/ 是正则，未加 ^$ 时在消息里任意位置命中即可，例如 /^签到\\s*\\d*$/；/表达式/i 表示忽略大小写。'}
      </p>
    </div>
  )
}
