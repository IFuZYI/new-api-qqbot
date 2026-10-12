/**
 * LogsView is the 日志 tab: what the service has been doing, read straight from
 * the daily files.
 *
 * The page exists because a failure used to be visible only while someone was
 * watching the terminal, or as one line in a toast that scrolled away. Here the
 * operator picks a day, filters to a level, searches for a QQ number or an error
 * string, and reads the whole record with its attributes.
 *
 * Level filter and search are applied server-side (the file can be tens of
 * megabytes), so the counts shown next to the filter always describe the whole
 * day rather than the filtered result — "3 errors today" stays true while the
 * list is filtered to WARN.
 */
import { useCallback, useEffect, useRef, useState } from 'react'
import { api, type LogDay, type LogEntry, type LogResponse } from '../api'
import { Badge, Button, Card, Empty, ErrorNote, Field, Input, Select, Table, Cell } from '../ui'
import { formatDateTime } from '../format'

/** Levels the filter offers, strongest first. */
const LEVELS = [
  { value: '', label: '全部等级' },
  { value: 'debug', label: 'DEBUG 及以上' },
  { value: 'info', label: 'INFO 及以上' },
  { value: 'warn', label: 'WARN 及以上' },
  { value: 'error', label: '仅 ERROR' },
] as const

const LEVEL_TONE: Record<string, 'ok' | 'warn' | 'err' | 'info'> = {
  DEBUG: 'info',
  INFO: 'ok',
  WARN: 'warn',
  ERROR: 'err',
}

export default function LogsView({ onFail }: { onFail: (e: unknown) => void }) {
  const [res, setRes] = useState<LogResponse | null>(null)
  const [days, setDays] = useState<LogDay[]>([])
  const [dir, setDir] = useState('')
  // The day boundary is the operator's timezone, so the times are rendered in
  // it too: with the browser in another zone, a record filed under the
  // operator's day would otherwise read as a time on the day before.
  const [tz, setTz] = useState('')
  // today is the server's today, kept apart from the selected day: the 今天
  // option's value is '' and its label must name the day that value selects.
  const [today, setToday] = useState('')
  const [day, setDay] = useState('')
  const [level, setLevel] = useState('')
  const [search, setSearch] = useState('')
  // inFlight counts requests, so the 刷新 button stays disabled while ANY query
  // is outstanding: a boolean set/cleared per request re-enabled it as soon as
  // the first of two overlapping requests settled.
  const [inFlight, setInFlight] = useState(0)
  const busy = inFlight > 0
  const [err, setErr] = useState('')

  // daysRefreshedAt throttles the day-list request: it only changes when a day
  // rolls over or a file is written, so re-reading it on every keystroke of a
  // search is pure waste.
  const daysRefreshedAt = useRef(0)

  // reqSeq is the request generation. Changing the filter while a request is in
  // flight leaves two responses racing, and the one that resolves LAST would win
  // — which can be the older one (a slow ERROR query answering after a fast
  // DEBUG one), leaving the rows showing one filter while the control shows
  // another. Only the newest request is allowed to write state.
  const reqSeq = useRef(0)

  const load = useCallback(
    async (opts: { day: string; level: string; q: string }) => {
      const seq = ++reqSeq.current
      setInFlight((n) => n + 1)
      try {
        const r = await api.logs({
          day: opts.day || undefined,
          level: (opts.level || undefined) as never,
          q: opts.q || undefined,
        })
        if (seq !== reqSeq.current) return
        setRes(r)
        setErr('')
        // Only a query with no day parameter answers for TODAY: r.day is the
        // day that was asked for, so reading it unconditionally made the 今天
        // option claim a past day was today.
        if (!opts.day && r.day) setToday(r.day)
        if (r.tz) setTz(r.tz)
        // Refresh the picker at most once a minute: it is a directory listing,
        // not part of the query.
        if (Date.now() - daysRefreshedAt.current > 60_000) {
          const d = await api.logDays()
          // Arm the throttle only after the listing landed: arming it first
          // locked the picker out for a minute after a FAILED listing, with the
          // directory hint left blank and nothing to select.
          daysRefreshedAt.current = Date.now()
          if (seq !== reqSeq.current) return
          setDays(d.days ?? [])
          setDir(d.dir)
          if (d.tz) setTz(d.tz)
        }
      } catch (e) {
        if (seq !== reqSeq.current) return
        setErr(e instanceof Error ? e.message : String(e))
        onFail(e)
      } finally {
        setInFlight((n) => n - 1)
      }
    },
    [onFail],
  )

  // The first load asks for today (no day parameter): the day picker's own
  // value is empty until the server says which day that is.
  useEffect(() => {
    daysRefreshedAt.current = 0
    void load({ day: '', level: '', q: '' })
  }, [load])

  const apply = (next: Partial<{ day: string; level: string; q: string }>) => {
    // The 今天 option carries value '' and means "no day parameter" — that is
    // what makes the server answer for today.
    const q = { day, level, q: search, ...next }
    if (next.day !== undefined) setDay(next.day)
    if (next.level !== undefined) setLevel(next.level)
    if (next.q !== undefined) setSearch(next.q)
    void load(q)
  }

  const counts = res?.counts ?? {}
  const total = Object.values(counts).reduce((a, b) => a + b, 0)
  // entries is read from the response, which is not guaranteed to match the
  // declared type: a partial or future payload (or a 200 from a proxy) with no
  // `entries` used to throw on `.length` during render, and with no error
  // boundary that unmounted the ENTIRE panel to a blank page. An absent list
  // degrades to "nothing to show" instead.
  const entries = res?.entries ?? []

  return (
    <div className="space-y-4">
      <Card
        title="日志"
        description="服务把每一条记录同时写到按天切分的文件和控制台。日志等级在「监测设置」里切换；这里只读。"
        actions={
          <Button size="sm" disabled={busy} onClick={() => apply({})}>
            {busy ? '刷新中…' : '刷新'}
          </Button>
        }
      >
        <div className="grid gap-x-6 gap-y-4 sm:grid-cols-3">
          <Field label="日期" hint={`文件目录：${dir || '—'}`}>
            <Select
              value={day}
              onChange={(v) => apply({ day: v })}
              ariaLabel="日志日期"
              className="w-44"
            >
              {/* Labelled from the SERVER's today, not from the selected day:
                  res.day is the day the current query answered for, so after
                  picking a past day this option used to claim that past day was
                  today while its value ('') still means today. */}
              <option value="">{today ? `今天（${today}）` : '今天'}</option>
              {days.map((d) => (
                <option key={d.day} value={d.day}>
                  {d.day}（{formatSize(d.size)}）
                </option>
              ))}
            </Select>
          </Field>
          <Field label="等级" hint="过滤只看，不影响记录什么">
            <Select value={level} onChange={(v) => apply({ level: v })} ariaLabel="日志等级" className="w-44">
              {LEVELS.map((l) => (
                <option key={l.value} value={l.value}>
                  {l.label}
                </option>
              ))}
            </Select>
          </Field>
          <Field label="搜索" hint="匹配消息、属性名与属性值，例如 QQ 号、patrol、错误串">
            <form
              // Without preventDefault the browser performs a real navigation:
              // the SPA reloads, the filter is lost and the operator is back on
              // the 概览 tab. The form exists so Enter submits.
              onSubmit={(e) => {
                e.preventDefault()
                apply({})
              }}
            >
              <Input
                value={search}
                onChange={setSearch}
                placeholder="关键字，回车搜索"
                ariaLabel="日志搜索"
                className="w-full"
              />
            </form>
          </Field>
        </div>

        <div className="mt-3 flex flex-wrap items-center gap-2">
          <span className="text-xs text-muted-foreground">
            {res?.day ?? '今天'}
            {tz ? `（${tz}）` : ''} 共 {total} 条：
          </span>
          {(['ERROR', 'WARN', 'INFO', 'DEBUG'] as const).map((lv) => (
            <Badge key={lv} tone={counts[lv] ? LEVEL_TONE[lv] : undefined}>
              {lv} {counts[lv] ?? 0}
            </Badge>
          ))}
          {res?.truncated && <span className="text-2xs text-muted-foreground">只显示最新的若干条，请缩小范围</span>}
          {!!res?.unparsed && (
            <span className="text-2xs text-muted-foreground">
              其中 {res.unparsed} 条无法解析（半截记录），计入总数但不会列出
            </span>
          )}
          {res?.incomplete && (
            <span className="text-2xs text-destructive">
              读取中断：有一条超长记录，之后的记录未计入
            </span>
          )}
        </div>

        {err && <div className="mt-3"><ErrorNote>{err}</ErrorNote></div>}
      </Card>

      <Card title="记录">
        {!res ? (
          err ? (
            // A failed first load leaves res null; without this branch the card
            // said 载入中… forever while the error was already known.
            <div className="space-y-3">
              <ErrorNote>{err}</ErrorNote>
              <Button size="sm" onClick={() => apply({})}>
                重试
              </Button>
            </div>
          ) : (
            '载入中…'
          )
        ) : entries.length === 0 ? (
          <Empty>这一天没有符合条件的记录。</Empty>
        ) : (
          <Table head={['时间', '等级', '内容']}>
            {entries.map((e, i) => (
              <tr key={`${e.time}-${i}`} className="align-top">
                <Cell className="whitespace-nowrap text-muted-foreground tabular-nums">
                  {formatClock(e.time, tz)}
                </Cell>
                <Cell>
                  <Badge tone={LEVEL_TONE[e.level]}>{e.level}</Badge>
                </Cell>
                <Cell className="min-w-0">
                  <div className="break-all text-sm">{e.msg}</div>
                  <Attrs entry={e} />
                </Cell>
              </tr>
            ))}
          </Table>
        )}
      </Card>
    </div>
  )
}

/**
 * Attrs renders a record's extra fields. They are what makes a record usable:
 * the message says "巡检失败", the attributes say which patrol and why.
 */
function Attrs({ entry }: { entry: LogEntry }) {
  const attrs = entry.attrs
  if (!attrs || Object.keys(attrs).length === 0) return null
  return (
    <div className="mt-0.5 flex flex-wrap gap-x-3 gap-y-0.5 text-2xs text-muted-foreground">
      {Object.entries(attrs).map(([k, v]) => (
        <span key={k} className="break-all">
          <span className="font-medium">{k}</span>={clip(attrValue(v))}
        </span>
      ))}
    </div>
  )
}

/**
 * attrValue renders one attribute value.
 *
 * String(v) turns a nested object into "[object Object]" and an array into a
 * bare comma-joined list; JSON is what the file actually holds, so it is what
 * the reader should see. Numbers and booleans keep their plain form.
 */
function attrValue(v: unknown): string {
  if (v === null || v === undefined) return String(v)
  if (typeof v === 'object') {
    try {
      return JSON.stringify(v)
    } catch {
      return String(v)
    }
  }
  return String(v)
}

/**
 * clip bounds one attribute value. A gateway error is a paragraph, and printing
 * it in full pushed everything else off the row; the full text is still in the
 * file, and the search matches it.
 */
function clip(s: string, max = 240): string {
  return s.length > max ? `${s.slice(0, max)}…` : s
}

/**
 * formatClock renders a record's timestamp in the operator's timezone, which is
 * the calendar the selected day is in.
 */
function formatClock(iso: string, tz: string): string {
  if (!iso) return '—'
  const t = Date.parse(iso)
  if (Number.isNaN(t)) return iso
  // A zero timestamp (a producer that wrote no time) reads as 1/01/01 in the
  // operator's zone; an em dash says "not recorded" instead of inventing a date.
  if (t <= 0) return '—' 
  if (!tz) return formatDateTime(Math.floor(t / 1000))
  try {
    return new Intl.DateTimeFormat('zh-CN', {
      timeZone: tz,
      year: 'numeric', month: '2-digit', day: '2-digit',
      hour: '2-digit', minute: '2-digit', second: '2-digit',
      hour12: false,
    }).format(new Date(t))
  } catch {
    // An unknown zone name must not blank the row: fall back to local time.
    return formatDateTime(Math.floor(t / 1000))
  }
}

function formatSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`
}
