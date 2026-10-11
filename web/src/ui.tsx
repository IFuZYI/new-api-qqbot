import { createContext, useCallback, useContext, useEffect, useId, useMemo, useRef, useState, type ReactNode } from 'react'

/**
 * Shared UI kit. Colours come from the token layer in index.css (new-api's
 * palette), so every component follows the light/dark switch automatically and
 * no component hard-codes a colour.
 *
 * Sizing follows the same rule: components name a role (`rounded-control`,
 * `h-8`) rather than a raw step, so a form cannot drift into five unrelated
 * field widths or two button heights. The radius ladder is
 * surface > panel > control > chip.
 */

/**
 * Focus ring shared by every interactive element.
 *
 * Deliberately does NOT include `outline-none`: that utility sets
 * `--tw-outline-style: none`, and Tailwind's `focus-visible:outline-2` reads
 * `outline-style: var(--tw-outline-style)` — so the ring silently stayed
 * suppressed (measured: `outline: none 0px` on exactly the buttons carrying
 * this class, while unclassed ones got the global ring). The base-layer
 * `:focus-visible` rule in index.css supplies the ring for everything.
 */
const FOCUS = 'focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[var(--ring)]'

/**
 * The closed dropdown control, shared by Select, Combobox and MultiSelect so
 * the three cannot drift apart (they had already grown two left paddings).
 *
 * `pr-7` reserves the chevron's lane: the chevron is absolutely positioned by
 * Chevron below, so the text would otherwise run underneath it.
 */
const CONTROL =
  'h-8 w-full max-w-full min-w-0 rounded-control border border-input bg-background pl-2.5 pr-7 text-sm text-foreground transition-colors hover:border-ring/60 disabled:cursor-not-allowed disabled:opacity-60'

/** The dropdown panel chrome, shared by Combobox and MultiSelect. */
const PANEL =
  'scroll-slim absolute z-30 mt-1 max-h-56 w-full overflow-y-auto rounded-panel border border-border bg-popover p-1 shadow-lg'

/** One selectable row inside a dropdown panel. */
const OPTION = 'cursor-pointer rounded-sm px-2 py-1.5 text-sm transition-colors hover:bg-accent'

/**
 * The dropdown chevron, drawn from the tokens rather than the OS glyph.
 *
 * Absolutely positioned at the right edge of its (relatively positioned)
 * control, so all three dropdowns place it identically. It is
 * pointer-events-none: the control underneath must stay clickable. When the
 * control is disabled the chevron dims with it, because the control's own
 * disabled:opacity-60 cannot reach a sibling.
 */
function Chevron({ open, disabled }: { open?: boolean; disabled?: boolean }) {
  return (
    <span className="pointer-events-none absolute top-1/2 right-2.5 -translate-y-1/2">
      <svg
        aria-hidden="true"
        viewBox="0 0 12 12"
        className={`size-3 text-muted-foreground transition-transform ${open ? 'rotate-180' : ''} ${disabled ? 'opacity-60' : ''}`}
      >
        <path d="M2.5 4.5 6 8l3.5-3.5" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" />
      </svg>
    </span>
  )
}

export function Card({
  title,
  children,
  actions,
  description,
  className = '',
}: {
  /**
   * A node rather than a string so a card can carry a badge beside its name
   * (e.g. 已停用). aria-labelledby still names the section from the h2, and the
   * badge text is part of it, which is what a screen reader should hear.
   */
  title?: ReactNode
  children: ReactNode
  actions?: ReactNode
  /** Optional one-line subtitle under the title. */
  description?: string
  className?: string
}) {
  const id = useId()
  return (
    <section
      aria-labelledby={title ? id : undefined}
      className={`rounded-surface border border-border bg-card text-card-foreground shadow-sm ${className}`}
    >
      {(title || actions) && (
        <header className="flex flex-wrap items-center justify-between gap-x-3 gap-y-2 border-b border-border px-4 py-3">
          <div className="min-w-0">
            {title && (
              <h2 id={id} className="text-sm font-semibold tracking-tight">
                {title}
              </h2>
            )}
            {description && <p className="mt-0.5 text-xs text-muted-foreground">{description}</p>}
          </div>
          {actions && <div className="flex flex-wrap items-center gap-2">{actions}</div>}
        </header>
      )}
      <div className="p-4">{children}</div>
    </section>
  )
}

export function Button({
  children,
  onClick,
  kind = 'default',
  disabled,
  type = 'button',
  size = 'md',
  title,
  className = '',
  full,
}: {
  children: ReactNode
  onClick?: () => void
  kind?: 'default' | 'primary' | 'danger' | 'ghost'
  disabled?: boolean
  type?: 'button' | 'submit'
  size?: 'sm' | 'md'
  title?: string
  className?: string
  /** Stretch to the container (used by dialog actions on narrow screens). */
  full?: boolean
}) {
  const styles: Record<string, string> = {
    primary:
      'border-transparent bg-primary text-primary-foreground shadow-xs hover:bg-primary/90 active:bg-primary/95',
    default: 'border-border bg-background text-foreground hover:bg-accent hover:text-accent-foreground',
    danger:
      'border-transparent bg-destructive/10 text-destructive hover:bg-destructive/20 active:bg-destructive/25',
    ghost:
      'border-transparent bg-transparent text-muted-foreground hover:bg-accent hover:text-accent-foreground',
  }
  const sizes: Record<string, string> = {
    sm: 'h-7 gap-1.5 px-2 text-xs',
    md: 'h-8 gap-1.5 px-3 text-xs',
  }
  return (
    <button
      type={type}
      title={title}
      onClick={onClick}
      disabled={disabled}
      className={`inline-flex shrink-0 items-center justify-center rounded-control border font-medium whitespace-nowrap transition-[background-color,color,box-shadow,opacity] disabled:pointer-events-none disabled:opacity-50 ${sizes[size]} ${styles[kind]} ${FOCUS} ${full ? 'w-full' : ''} ${className}`}
    >
      {children}
    </button>
  )
}

export function Field({
  label,
  hint,
  children,
}: {
  label: string
  hint?: string
  children: ReactNode
}) {
  return (
    // The label WRAPS the control (implicit association) rather than sitting
    // beside it. A sibling <label htmlFor> only associates when the caller also
    // threads an id onto the control, and no call site does — measured: all 11
    // controls on 连接配置 had an empty accessible name and clicking a label
    // focused nothing, so a screen reader read "edit text, blank" for every
    // field. Wrapping associates every field with no call-site change.
    <label className="flex min-w-0 flex-col gap-1.5">
      <span className="text-xs font-medium text-foreground/80">{label}</span>
      {children}
      {hint && <span className="text-[11px] leading-4 text-muted-foreground">{hint}</span>}
    </label>
  )
}

export function Input({
  value,
  onChange,
  type = 'text',
  placeholder,
  id,
  disabled,
  invalid,
  min,
  max,
  autoFocus,
  autoComplete,
  inputMode,
  className = '',
  ariaLabel,
}: {
  value: string | number
  onChange: (v: string) => void
  type?: string
  placeholder?: string
  id?: string
  disabled?: boolean
  invalid?: boolean
  min?: number
  max?: number
  autoFocus?: boolean
  autoComplete?: string
  inputMode?: 'text' | 'numeric' | 'decimal'
  className?: string
  ariaLabel?: string
}) {
  // No `w-full` in the base: Field is a flex column, so the control already
  // stretches to the field width, and a hard-coded 100% here would outrank any
  // `w-*` the caller passes (measured: a `w-20` interval input rendered 203px).
  return (
    <input
      id={id}
      type={type}
      value={value}
      placeholder={placeholder}
      disabled={disabled}
      min={min}
      max={max}
      autoFocus={autoFocus}
      autoComplete={autoComplete}
      inputMode={inputMode}
      aria-label={ariaLabel}
      aria-invalid={invalid || undefined}
      onChange={(e) => onChange(e.target.value)}
      className={`h-8 max-w-full min-w-0 rounded-control border bg-background px-2.5 text-sm text-foreground transition-colors placeholder:text-muted-foreground disabled:cursor-not-allowed disabled:opacity-60 ${
        invalid ? 'border-destructive' : 'border-input hover:border-ring/60'
      } ${FOCUS} ${className}`}
    />
  )
}

/**
 * Select stays a native <select> — the dropdown is the browser's, so it keeps
 * native keyboard and screen-reader behaviour. The OS arrow is the one part
 * that cannot be themed, so it is removed (appearance-none) and redrawn from
 * the tokens; the chevron is pointer-events-none so the whole control stays
 * clickable.
 *
 * The wrapper is inline-block on purpose: in a table cell the control used to
 * size to its content, and a block wrapper would stretch it across the column.
 * Inside a Field (a flex column) the wrapper is blockified and stretches, and
 * the select fills it via w-full — the layout those call sites already had.
 */
export function Select({
  value,
  onChange,
  children,
  className = '',
  id,
  disabled,
  ariaLabel,
}: {
  value: string | number
  onChange: (v: string) => void
  children: ReactNode
  className?: string
  id?: string
  disabled?: boolean
  ariaLabel?: string
}) {
  return (
    <div className="relative inline-block max-w-full min-w-0">
      <select
        id={id}
        value={value}
        disabled={disabled}
        aria-label={ariaLabel}
        onChange={(e) => onChange(e.target.value)}
        className={`block cursor-pointer appearance-none py-0 ${CONTROL} ${FOCUS} ${className}`}
      >
        {children}
      </select>
      <Chevron disabled={disabled} />
    </div>
  )
}

export type ComboOption = {
  value: string
  /** Shown in the input once selected, and in the list. */
  label: string
  /** Extra strings the filter matches on (email, id, nickname). */
  keywords?: string
}

/**
 * Combobox is a searchable single-select: a text input that opens a filtered
 * list, for fields whose options are too many to scan (a whole roster) or whose
 * stored value is not the one the operator recognises (a QQ number vs a name).
 *
 * It follows MultiSelect's dropdown rules — outside click and Escape close it,
 * one box ref, listbox roles, the document listener only while open — and adds
 * the highlight held in state, because a text input has no native listbox
 * behaviour to lean on.
 */
export function Combobox({
  value,
  onChange,
  options,
  placeholder,
  emptyText = '无匹配项',
  ariaLabel,
  disabled,
  id,
  className = '',
}: {
  value: string
  onChange: (v: string) => void
  options: ComboOption[]
  placeholder?: string
  emptyText?: string
  ariaLabel?: string
  disabled?: boolean
  id?: string
  className?: string
}) {
  const [open, setOpen] = useState(false)
  // The text is only "live" while the list is open. Closed, the input is
  // derived from `value` (see `shown`) — so a half-typed filter can never be
  // left on screen looking like a committed selection, and Escape/blur simply
  // drop the typed text without extra state juggling.
  const [text, setText] = useState('')
  const [hi, setHi] = useState(0)
  const box = useRef<HTMLDivElement>(null)
  const input = useRef<HTMLInputElement>(null)
  const uid = useId()
  const listId = `${uid}-list`
  const optId = (i: number) => `${uid}-opt-${i}`

  // The caller may hold a value the list does not carry (a stale account), so
  // the raw value is the fallback label rather than an empty box.
  const committed = options.find((o) => o.value === value)
  const committedLabel = committed ? committed.label : value
  const shown = open ? text : committedLabel

  // An untouched label is not a query: filtering by it would open the list
  // with only the current row in it, which reads as "there is nothing else".
  const query = open && text !== committedLabel ? text.trim().toLowerCase() : ''
  const filtered = query
    ? options.filter(
        (o) =>
          o.label.toLowerCase().includes(query) ||
          (o.keywords ? o.keywords.toLowerCase().includes(query) : false),
      )
    : options
  // Clamp on read: the option list can shrink under an open dropdown (a sync
  // finishing), and an out-of-range index would make Enter commit nothing.
  const hiIdx = filtered.length === 0 ? -1 : Math.min(hi, filtered.length - 1)

  // Close on an outside click, so the dropdown cannot get stuck open. Added
  // only while open and removed on cleanup, like MultiSelect.
  useEffect(() => {
    if (!open) return
    const onDown = (e: MouseEvent) => {
      if (box.current && !box.current.contains(e.target as Node)) setOpen(false)
    }
    document.addEventListener('mousedown', onDown)
    return () => document.removeEventListener('mousedown', onDown)
  }, [open])

  // Keep the highlighted row in view as the highlight moves — or as typing
  // re-shapes the list under it.
  useEffect(() => {
    if (!open) return
    box.current?.querySelector<HTMLElement>('[data-active="true"]')?.scrollIntoView({ block: 'nearest' })
  }, [open, hiIdx, text])

  const openList = () => {
    if (disabled) return
    setText(committedLabel)
    // Highlight the current value, not the first row. A native select opens on
    // the current option, and this control replaced one: opening and pressing
    // Enter (or an ArrowDown meaning "next after the current") must not land on
    // an unrelated row.
    const i = options.findIndex((o) => o.value === value)
    setHi(i >= 0 ? i : 0)
    setOpen(true)
    // Select the label so the first keystroke replaces it. This is safe because
    // the mousedown that opens the list suppresses the browser's caret
    // placement (see the input's onMouseDown), which would otherwise undo it.
    input.current?.select()
  }

  const commit = (opt: ComboOption) => {
    onChange(opt.value)
    setOpen(false)
    setHi(0)
  }

  const onKeyDown = (e: React.KeyboardEvent<HTMLInputElement>) => {
    // A composing IME confirms with Enter and walks its candidates with the
    // arrow keys; those keystrokes belong to the IME, not to the list. Without
    // this guard a Chinese-input operator pressing Enter to accept a pinyin
    // candidate commits whichever option is highlighted — silently replacing
    // the selection, and Save then writes the wrong binding. keyCode 229 covers
    // Safari, which reports the composition keys out of order.
    if (e.nativeEvent.isComposing || e.nativeEvent.keyCode === 229) return

    if (e.key === 'Escape') {
      if (!open) return
      // The dialog behind us also closes on Escape; stopping the event here is
      // what keeps Escape from dismissing the list and the modal together.
      e.stopPropagation()
      setOpen(false)
      setHi(0)
      return
    }
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      e.preventDefault() // the caret must not jump to either end of the text
      if (!open) {
        openList()
        return
      }
      if (filtered.length === 0) return
      const step = e.key === 'ArrowDown' ? 1 : -1
      setHi((hiIdx + step + filtered.length) % filtered.length)
      return
    }
    if (e.key === 'Enter') {
      // While the list is open Enter belongs to the list, so it must not reach
      // an enclosing form and submit it. With no matches there is nothing to
      // commit: closing would leave the typed filter looking like a selection.
      if (!open) return
      e.preventDefault()
      if (hiIdx < 0) return
      commit(filtered[hiIdx])
    }
  }

  return (
    <div ref={box} className="relative inline-block max-w-full min-w-0">
      <input
        ref={input}
        id={id}
        type="text"
        role="combobox"
        aria-expanded={open}
        // ARIA 1.2 makes aria-controls required on a combobox whenever
        // aria-expanded is true, so it points at whichever popup is actually
        // rendered: the listbox, or the no-match panel — which carries the same
        // id, because the requirement is that the reference resolves, not that
        // the target's role be listbox. Pointing at nothing (as this did in the
        // no-match state) is what axe flags, critical.
        aria-controls={open ? listId : undefined}
        aria-activedescendant={open && hiIdx >= 0 ? optId(hiIdx) : undefined}
        aria-autocomplete="list"
        aria-label={ariaLabel}
        autoComplete="off"
        disabled={disabled}
        value={shown}
        placeholder={placeholder}
        onChange={(e) => {
          setText(e.target.value)
          setHi(0)
          // Reopen only while the field still has focus. An IME commit can
          // deliver its input event after a click-away blur, and reopening then
          // leaves the list floating over the page with no focus and a stale
          // filter. Typing after Escape still reopens, because focus stays put.
          if (document.activeElement === input.current) setOpen(true)
        }}
        onFocus={openList}
        // The browser's default caret placement runs after focus and would land
        // *after* openList's select() (measured: caret at the end of the label,
        // so the first keystroke appends instead of replacing). Taking over the
        // gesture when closed keeps the label selected; once open the default
        // is left alone so the caret and drag-select inside the typed filter
        // still work.
        onMouseDown={(e) => {
          if (open || disabled) return
          e.preventDefault()
          if (document.activeElement !== input.current) input.current?.focus()
          else openList()
        }}
        onClick={() => {
          if (!open) openList()
        }}
        onBlur={() => setOpen(false)}
        onKeyDown={onKeyDown}
        className={`block placeholder:text-muted-foreground ${CONTROL} ${FOCUS} ${className}`}
      />
      <Chevron open={open} disabled={disabled} />
      {open &&
        (filtered.length === 0 ? (
          // No matches: render the message as its own panel, NOT inside the
          // listbox. A listbox may only contain options, so a message inside it
          // is invalid markup and screen readers announce an empty list instead
          // of the explanation. role="status" announces the no-match case.
          //
          // It carries the listbox's id because aria-controls must resolve to
          // whatever popup is open; the id names the popup, not the role.
          <div id={listId} className={PANEL}>
            <p role="status" className="px-2 py-1.5 text-xs text-muted-foreground">
              {emptyText}
            </p>
          </div>
        ) : (
          <div id={listId} role="listbox" className={PANEL}>
            {filtered.map((o, i) => (
              <div
                // Two QQs may be bound to one account, which makes their
                // account options share a value; index keeps the keys unique.
                key={`${i}:${o.value}`}
                id={optId(i)}
                role="option"
                aria-selected={i === hiIdx}
                data-active={i === hiIdx ? 'true' : undefined}
                // mousedown must not move focus out of the input: blur would
                // close the list and snap the box back to the committed label
                // before the click lands, so the click would commit against a
                // filter that is already gone. Keeping focus here leaves the
                // click as the only thing that commits.
                onMouseDown={(e) => e.preventDefault()}
                onMouseEnter={() => setHi(i)}
                onClick={() => commit(o)}
                className={`${OPTION} ${i === hiIdx ? 'bg-accent text-accent-foreground' : 'text-foreground'}`}
              >
                {o.label}
              </div>
            ))}
          </div>
        ))}
    </div>
  )
}

/**
 * MultiSelect is a checkbox dropdown for "pick any number of these" fields.
 *
 * A native <select multiple> is unusable here: it needs ctrl-click to add a
 * second option and shows every choice as a permanently highlighted row. This
 * keeps the closed state to one compact line (with the selection summarised)
 * and opens a list of checkboxes, which is what the choice actually is.
 */
export function MultiSelect({
  options,
  selected,
  onChange,
  emptyText = '无可选项',
  allText = '任意',
  disabled,
  ariaLabel,
}: {
  options: string[]
  selected: string[]
  onChange: (next: string[]) => void
  /** Shown inside the closed control when nothing is selected. */
  allText?: string
  /** Shown when there are no options at all. */
  emptyText?: string
  disabled?: boolean
  ariaLabel?: string
}) {
  const [open, setOpen] = useState(false)
  const box = useRef<HTMLDivElement>(null)
  const btn = useRef<HTMLButtonElement>(null)

  // Close on an outside click or Escape, so the dropdown cannot get stuck open.
  useEffect(() => {
    if (!open) return
    const onDown = (e: MouseEvent) => {
      if (box.current && !box.current.contains(e.target as Node)) setOpen(false)
    }
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== 'Escape') return
      // Capture phase, and stopPropagation: a dropdown opened inside a Modal
      // must consume the first Escape itself. React attaches the Modal's own
      // onKeyDown at the root container, which runs before this document-level
      // bubble listener ever sees the event — measured: one Escape closed both
      // the dropdown AND the dialog, discarding the form being edited. Capture
      // runs root->target, so it fires first and the Modal keeps its state.
      e.stopPropagation()
      setOpen(false)
      btn.current?.focus()
    }
    document.addEventListener('mousedown', onDown)
    document.addEventListener('keydown', onKey, true)
    return () => {
      document.removeEventListener('mousedown', onDown)
      document.removeEventListener('keydown', onKey, true)
    }
  }, [open])

  const toggle = (opt: string) =>
    onChange(selected.includes(opt) ? selected.filter((x) => x !== opt) : [...selected, opt])

  const summary = selected.length === 0 ? allText : selected.join('、')

  return (
    <div ref={box} className="relative min-w-0">
      <button
        ref={btn}
        type="button"
        disabled={disabled}
        onClick={() => setOpen((v) => !v)}
        aria-expanded={open}
        aria-label={ariaLabel}
        className={`flex items-center justify-between gap-2 text-left ${CONTROL} ${FOCUS}`}
      >
        <span className={`min-w-0 truncate ${selected.length === 0 ? 'text-muted-foreground' : ''}`}>
          {summary}
        </span>
        {selected.length > 0 && <Badge tone="accent">{selected.length}</Badge>}
      </button>
      <Chevron open={open} disabled={disabled} />

      {open && (
        // A checkbox group, not a listbox: the rows are real <input
        // type="checkbox"> elements, so they carry their own state and keyboard
        // behaviour. Marking this container role="listbox" (as it did) is
        // invalid markup — a listbox may only own options — and axe flagged it
        // critical (aria-required-children); the listbox also exposed 0 options,
        // so the checkboxes were not reachable through it. role="group" names
        // the popup without lying about its contents.
        <div role="group" aria-label={ariaLabel} className={PANEL}>
          {options.length === 0 ? (
            <p className="px-2 py-1.5 text-xs text-muted-foreground">{emptyText}</p>
          ) : (
            <>
              {selected.length > 0 && (
                <button
                  type="button"
                  onClick={() => onChange([])}
                  className={`mb-1 w-full rounded-sm px-2 py-1.5 text-left text-xs text-muted-foreground transition-colors hover:bg-accent ${FOCUS}`}
                >
                  清除选择（= {allText}）
                </button>
              )}
              {options.map((opt) => (
                <label
                  key={opt}
                  className={`flex items-center gap-2 ${OPTION}`}
                >
                  <input
                    type="checkbox"
                    className="size-4 shrink-0"
                    checked={selected.includes(opt)}
                    onChange={() => toggle(opt)}
                  />
                  <span className="min-w-0 truncate">{opt}</span>
                </label>
              ))}
            </>
          )}
        </div>
      )}
    </div>
  )
}

/**
 * Switch is the sliding on/off control used for the patrol toggles.
 *
 * It is a real <button role="switch">, not a styled checkbox: the state is then
 * announced to assistive tech and reachable by keyboard, and the visible track
 * and knob are drawn from the theme tokens so both light and dark follow
 * automatically. The knob is positioned by transform (not left/right) so the
 * transition animates instead of jumping.
 *
 * The hit area is padded to 32px tall even though the track is 20px, so the
 * control clears the 24x24 minimum target size (WCAG 2.5.8) on touch screens.
 */
export function Switch({
  checked,
  onChange,
  label,
  disabled,
  id,
  ariaLabel,
}: {
  checked: boolean
  onChange: (v: boolean) => void
  /** Accessible name; also rendered beside the control. */
  label?: string
  disabled?: boolean
  id?: string
  /**
   * Overrides the accessible name. Needed where the visible label is only the
   * state ("已启用"), which would leave several switches on one page sharing an
   * accessible name.
   */
  ariaLabel?: string
}) {
  return (
    <button
      id={id}
      type="button"
      role="switch"
      aria-checked={checked}
      aria-label={ariaLabel ?? label}
      disabled={disabled}
      onClick={() => onChange(!checked)}
      className={`group inline-flex min-h-8 items-center gap-2 disabled:cursor-not-allowed disabled:opacity-50 ${FOCUS} rounded-control`}
    >
      <span
        className={`relative inline-flex h-5 w-9 shrink-0 items-center rounded-full border transition-colors ${
          checked ? 'border-transparent bg-primary' : 'border-border bg-muted'
        }`}
      >
        <span
          className={`pointer-events-none block size-3.5 rounded-full bg-background shadow-sm transition-transform ${
            checked ? 'translate-x-[18px]' : 'translate-x-[3px]'
          }`}
        />
      </span>
      {label && (
        <span className={`text-xs ${checked ? 'text-foreground' : 'text-muted-foreground'}`}>
          {label}
        </span>
      )}
    </button>
  )
}

export function Badge({
  children,
  tone = 'muted',
}: {
  children: ReactNode
  tone?: 'ok' | 'warn' | 'err' | 'muted' | 'accent' | 'info'
}) {
  const tones: Record<string, string> = {
    ok: 'text-success border-success/40 bg-success/10',
    warn: 'text-warning border-warning/40 bg-warning/10',
    err: 'text-destructive border-destructive/40 bg-destructive/10',
    accent: 'text-primary border-primary/40 bg-primary/10',
    info: 'text-info border-info/40 bg-info/10',
    muted: 'text-muted-foreground border-border bg-muted',
  }
  return (
    <span
      className={`inline-flex items-center rounded-chip border px-1.5 py-px text-[11px] leading-4 font-medium whitespace-nowrap ${tones[tone]}`}
    >
      {children}
    </span>
  )
}

/**
 * Table renders a data grid with an optional column pinned to the inline end.
 *
 * `stickyEnd` exists for the action column: on a phone the buttons otherwise
 * sit past the viewport edge with no hint they are there (measured at
 * x=769-853px inside a 390px viewport), so the operator sees a table with no
 * way to act on a row.
 */
export function Table({
  head,
  children,
  stickyEnd,
  className = '',
}: {
  head: ReactNode[]
  children: ReactNode
  /** Number of trailing columns to pin while the table scrolls sideways. */
  stickyEnd?: number
  className?: string
}) {
  const n = head.length
  const isSticky = (i: number) => stickyEnd !== undefined && i >= n - stickyEnd
  return (
    <div className={`scroll-slim -mx-4 overflow-x-auto px-4 sm:mx-0 sm:px-0 ${className}`}>
      <table className="w-full border-collapse text-sm">
        <thead>
          <tr className="text-left text-xs text-muted-foreground">
            {head.map((h, i) => (
              <th
                key={i}
                scope="col"
                className={`border-b border-border bg-[var(--table-header)] px-3 py-2 font-medium whitespace-nowrap ${
                  isSticky(i) ? 'cell-sticky-head' : ''
                }`}
              >
                {h}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>{children}</tbody>
      </table>
    </div>
  )
}

/** One row of a data table; pairs with Table's sticky-end support. */
export function Row({
  children,
  className = '',
}: {
  children: ReactNode
  className?: string
}) {
  return <tr className={`data-row border-b border-border/60 ${className}`}>{children}</tr>
}

/** A cell. `sticky` pins it to the inline end (see Table's stickyEnd). */
export function Cell({
  children,
  sticky,
  className = '',
  align,
}: {
  children: ReactNode
  sticky?: boolean
  className?: string
  align?: 'right'
}) {
  return (
    <td
      className={`px-3 py-2 align-middle ${sticky ? 'cell-sticky' : ''} ${
        align === 'right' ? 'text-right' : ''
      } ${className}`}
    >
      {children}
    </td>
  )
}

export function Empty({ children }: { children: ReactNode }) {
  return (
    <div className="flex flex-col items-center gap-2 py-10 text-center">
      <svg aria-hidden="true" viewBox="0 0 24 24" className="size-6 text-muted-foreground/60">
        <path
          d="M4 7.5A2.5 2.5 0 0 1 6.5 5h11A2.5 2.5 0 0 1 20 7.5v9A2.5 2.5 0 0 1 17.5 19h-11A2.5 2.5 0 0 1 4 16.5v-9Z"
          fill="none"
          stroke="currentColor"
          strokeWidth="1.5"
        />
        <path d="M4 10h16" stroke="currentColor" strokeWidth="1.5" />
      </svg>
      <p className="max-w-md text-xs leading-5 text-muted-foreground">{children}</p>
    </div>
  )
}

export function ErrorNote({ children, onClose }: { children: ReactNode; onClose?: () => void }) {
  return (
    <div
      role="alert"
      className="mb-3 flex items-start justify-between gap-3 rounded-control border border-destructive/40 bg-destructive/10 px-3 py-2 text-xs text-destructive"
    >
      <span className="min-w-0 break-words">{children}</span>
      {onClose && (
        <button
          type="button"
          aria-label="关闭提示"
          onClick={onClose}
          className={`shrink-0 rounded-sm px-1 leading-none opacity-70 transition-opacity hover:opacity-100 ${FOCUS}`}
        >
          ✕
        </button>
      )}
    </div>
  )
}

/**
 * Inline validation / warning note used inside forms.
 *
 * Distinct from a toast on purpose: this marks a field or a form as currently
 * invalid and must persist while the operator fixes it, whereas a toast reports
 * something that already happened.
 */
export function WarnNote({ children }: { children: ReactNode }) {
  return (
    <p className="rounded-control border border-warning/40 bg-warning/10 px-3 py-2 text-xs text-warning">
      {children}
    </p>
  )
}

/* ---------------- toasts ---------------- */

type ToastTone = 'ok' | 'err' | 'info'

type Toast = {
  id: number
  tone: ToastTone
  message: string
}

type ToastAPI = {
  /** Report a completed action. Auto-dismisses. */
  success: (message: string) => void
  /** Report a failure. Stays longer, and never collapses into the success slot. */
  error: (message: string) => void
  info: (message: string) => void
}

const ToastContext = createContext<ToastAPI>({
  success: () => {},
  error: () => {},
  info: () => {},
})

/** useToast reports an action's outcome as a transient toast. */
export function useToast() {
  return useContext(ToastContext)
}

// An error is worth more reading time than a success, and a long one (an API
// error carries the server's message) more still.
const TOAST_MS: Record<ToastTone, number> = { ok: 4000, info: 5000, err: 9000 }

/** How many cards may be on screen at once. */
const TOAST_MAX = 4

/**
 * ToastProvider renders the toast stack and hands the API down. It lives above
 * the whole panel, so a message survives a tab change — the operator switches
 * tabs after acting, and the confirmation should follow them rather than
 * disappear with the unmounted view.
 */
export function ToastProvider({ children }: { children: ReactNode }) {
  const [toasts, setToasts] = useState<Toast[]>([])
  const nextID = useRef(1)
  // Track timers so an unmount does not leave callbacks running, and so closing
  // a toast by hand cancels its own dismissal.
  const timers = useRef(new Map<number, number>())
  // Mirror of the stack, kept so eviction can cancel the timer of the card it
  // drops: a state updater runs during render, which is too late to clear a
  // timeout, and the new list has to be known at push time.
  const stack = useRef<Toast[]>([])

  const dismiss = useCallback((id: number) => {
    const t = timers.current.get(id)
    if (t !== undefined) {
      window.clearTimeout(t)
      timers.current.delete(id)
    }
    stack.current = stack.current.filter((x) => x.id !== id)
    setToasts(stack.current)
  }, [])

  const push = useCallback(
    (tone: ToastTone) => (message: string) => {
      const text = message.trim()
      if (!text) return
      const id = nextID.current++
      const next = [...stack.current, { id, tone, message: text }]
      // Cap the stack: a burst of failures must not cover the page. The evicted
      // card's timer is cancelled here — leaving it would fire a dismissal
      // against a card that is no longer on screen, and a burst would pile up
      // orphaned timeouts.
      while (next.length > TOAST_MAX) {
        const gone = next.shift()!
        const t = timers.current.get(gone.id)
        if (t !== undefined) {
          window.clearTimeout(t)
          timers.current.delete(gone.id)
        }
      }
      stack.current = next
      setToasts(next)
      timers.current.set(id, window.setTimeout(() => dismiss(id), TOAST_MS[tone]))
    },
    [dismiss],
  )

  const api = useMemo<ToastAPI>(
    () => ({
      success: push('ok'),
      error: push('err'),
      info: push('info'),
    }),
    [push],
  )

  // Every card on screen must own a timer. StrictMode's simulated unmount runs
  // the cleanup below, which clears the map while the toasts state survives the
  // remount; without this, a toast pushed from a mount effect would stay on
  // screen forever. Re-arming also covers any later path that loses a timer.
  useEffect(() => {
    for (const t of toasts) {
      if (!timers.current.has(t.id)) {
        timers.current.set(t.id, window.setTimeout(() => dismiss(t.id), TOAST_MS[t.tone]))
      }
    }
  }, [toasts, dismiss])

  useEffect(
    () => () => {
      for (const t of timers.current.values()) window.clearTimeout(t)
      timers.current.clear()
    },
    [],
  )

  return (
    <ToastContext.Provider value={api}>
      {children}
      <div
        // Above the modal overlay (z-40) so a toast raised from a dialog is
        // still readable, and out of the pointer path except for its own
        // controls. Centred on every width: the top middle is where the eye
        // already is after an action, and a corner stack gets missed.
        className="pointer-events-none fixed inset-x-0 top-3 z-50 flex flex-col items-center gap-2 px-3"
        aria-live="polite"
        aria-atomic="false"
      >
        {toasts.map((t) => (
          <ToastCard key={t.id} toast={t} onDismiss={() => dismiss(t.id)} />
        ))}
      </div>
    </ToastContext.Provider>
  )
}

function ToastCard({ toast, onDismiss }: { toast: Toast; onDismiss: () => void }) {
  const tones: Record<ToastTone, { box: string; icon: ReactNode }> = {
    ok: {
      box: 'border-success/40 bg-success/10 text-success',
      icon: (
        <path d="M3 8.5 6.5 12 13 4.5" fill="none" stroke="currentColor" strokeWidth="1.75" strokeLinecap="round" strokeLinejoin="round" />
      ),
    },
    err: {
      box: 'border-destructive/40 bg-destructive/10 text-destructive',
      icon: (
        <path d="M8 4.5v4.5M8 11.4v.1" fill="none" stroke="currentColor" strokeWidth="1.75" strokeLinecap="round" />
      ),
    },
    info: {
      box: 'border-info/40 bg-info/10 text-info',
      icon: (
        <path d="M8 7v4.5M8 4.6v.1" fill="none" stroke="currentColor" strokeWidth="1.75" strokeLinecap="round" />
      ),
    },
  }
  const t = tones[toast.tone]
  return (
    <div
      // An error interrupts; a success waits its turn in the polite queue.
      role={toast.tone === 'err' ? 'alert' : 'status'}
      // The card stays click-through (the host is pointer-events-none) and only
      // the dismiss button opts back in. The stack floats over the header, so a
      // click-swallowing card silently eats taps on the tab row underneath it
      // (measured: clicking 连接配置 while a toast covered it did nothing; the
      // same click worked once the toast expired). A toast is not a modal — it
      // must never intercept a control the operator can see.
      className={`pointer-events-none flex w-full max-w-md items-start gap-2 rounded-panel border px-3 py-2 text-xs shadow-lg backdrop-blur-sm sm:w-auto ${t.box}`}
    >
      <svg aria-hidden="true" viewBox="0 0 16 16" className="mt-px size-4 shrink-0">
        {t.icon}
      </svg>
      <span className="min-w-0 flex-1 break-words">{toast.message}</span>
      <button
        type="button"
        aria-label="关闭提示"
        onClick={onDismiss}
        className={`pointer-events-auto -mr-1 shrink-0 rounded-sm p-0.5 opacity-70 transition-opacity hover:opacity-100 ${FOCUS}`}
      >
        <svg aria-hidden="true" viewBox="0 0 12 12" className="size-3">
          <path d="M2.5 2.5l7 7M9.5 2.5l-7 7" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" />
        </svg>
      </button>
    </div>
  )
}

/**
 * FieldAction places a control-less action (a button) inside a form grid so it
 * lines up with the inputs beside it.
 *
 * Aligning the *fields* does not align the *controls*: a Field whose hint is
 * set is taller, so bottom-aligning the row (items-end) pushes its input up by
 * exactly the hint's height (measured: 22px) and top-aligning it leaves the
 * bare button above the inputs. Reproducing Field's label line makes the action
 * share the inputs' baseline regardless of which siblings carry a hint.
 */
export function FieldAction({ children }: { children: ReactNode }) {
  return (
    <div className="flex min-w-0 flex-col gap-1.5">
      {/* Matches Field's label line box so the action sits level with the controls. */}
      <span aria-hidden="true" className="invisible text-xs font-medium">
        .
      </span>
      {children}
    </div>
  )
}

const FOCUSABLE =
  'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])'

/**
 * Modal shell shared by the dialogs.
 *
 * It is a real dialog: role/aria-modal so a screen reader announces it, focus
 * moved in on open and returned to the trigger on close, Tab cycling inside
 * (otherwise the keyboard wanders onto the page behind an overlay the user
 * cannot see through), Escape to dismiss, and background scroll locked.
 */
export function Modal({
  title,
  children,
  actions,
  onClose,
  wide,
  closeLabel = '关闭',
}: {
  title: string
  children: ReactNode
  actions?: ReactNode
  onClose: () => void
  wide?: boolean
  closeLabel?: string
}) {
  const panel = useRef<HTMLDivElement>(null)
  const titleID = useId()
  const restoreTo = useRef<HTMLElement | null>(null)

  // Remember what had focus so closing can hand it back.
  useEffect(() => {
    restoreTo.current = document.activeElement as HTMLElement | null
    return () => restoreTo.current?.focus?.()
  }, [])

  // Move focus into the dialog once it is mounted.
  useEffect(() => {
    const el = panel.current?.querySelector<HTMLElement>(FOCUSABLE)
    el?.focus()
  }, [])

  // Escape closes; Tab is trapped inside the dialog.
  const onKeyDown = useCallback(
    (e: React.KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.stopPropagation()
        onClose()
        return
      }
      if (e.key !== 'Tab') return
      const nodes = panel.current?.querySelectorAll<HTMLElement>(FOCUSABLE)
      if (!nodes || nodes.length === 0) return
      const first = nodes[0]
      const last = nodes[nodes.length - 1]
      const active = document.activeElement
      if (!e.shiftKey && active === last) {
        e.preventDefault()
        first.focus()
      } else if (e.shiftKey && active === first) {
        e.preventDefault()
        last.focus()
      }
    },
    [onClose],
  )

  // Lock the page behind the overlay.
  useEffect(() => {
    const prev = document.body.style.overflow
    document.body.style.overflow = 'hidden'
    return () => {
      document.body.style.overflow = prev
    }
  }, [])

  return (
    <div
      className="fixed inset-0 z-40 flex items-start justify-center overflow-y-auto bg-black/50 p-4 backdrop-blur-[1px]"
      onMouseDown={(e) => {
        if (e.target === e.currentTarget) onClose()
      }}
      onKeyDown={onKeyDown}
    >
      <div
        ref={panel}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleID}
        className={`my-8 w-full ${wide ? 'max-w-2xl' : 'max-w-md'}`}
      >
        <section className="rounded-surface border border-border bg-card text-card-foreground shadow-xl">
          <header className="flex items-start justify-between gap-3 border-b border-border px-4 py-3">
            <h2 id={titleID} className="text-sm font-semibold tracking-tight">
              {title}
            </h2>
            <button
              type="button"
              aria-label={closeLabel}
              onClick={onClose}
              className={`-m-1 shrink-0 rounded-sm p-1 text-muted-foreground transition-colors hover:bg-accent hover:text-accent-foreground ${FOCUS}`}
            >
              <svg aria-hidden="true" viewBox="0 0 14 14" className="size-3.5">
                <path d="M3 3l8 8M11 3l-8 8" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" />
              </svg>
            </button>
          </header>
          <div className="p-4">{children}</div>
          {actions && (
            <footer className="flex flex-wrap items-center justify-end gap-2 border-t border-border px-4 py-3">
              {actions}
            </footer>
          )}
        </section>
      </div>
    </div>
  )
}

/** A labelled block of related fields, used to group long forms. */
export function Fieldset({
  legend,
  children,
  hint,
}: {
  legend: string
  children: ReactNode
  hint?: string
}) {
  return (
    <fieldset className="rounded-panel border border-border px-3 pt-3 pb-3.5">
      <legend className="px-1 text-xs font-medium text-foreground/80">{legend}</legend>
      {hint && <p className="mb-3 text-[11px] leading-4 text-muted-foreground">{hint}</p>}
      <div className="space-y-3.5">{children}</div>
    </fieldset>
  )
}
