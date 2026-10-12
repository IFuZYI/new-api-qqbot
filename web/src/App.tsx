import { useCallback, useEffect, useRef, useState } from 'react'
import { api, SESSION_EXPIRED, type AuthState, type Bootstrap, type CheckResult, type Config, type ConfigRevisions, type Group, type Member, type MembersResponse, type UserFilter, type UserRow, type UsersResponse } from './api'
import Login from './Login'
import Rules from './Rules'
import LogsView from './features/Logs'
import SettingsView from './features/Settings'
import { ThemeToggle } from './theme'
import { Badge, Button, Card, Cell, Combobox, ConfirmDialog, Empty, ErrorNote, Field, FieldAction, Fieldset, Input, Modal, Note, Row, Select, Switch, TabBoundary, Table, useToast, WarnNote } from './ui'

type Tab = 'overview' | 'groups' | 'users' | 'rules' | 'settings' | 'logs' | 'connection'

const TABS: { id: Tab; label: string }[] = [
  { id: 'overview', label: '概览' },
  { id: 'groups', label: '群管理' },
  { id: 'users', label: '用户管理' },
  { id: 'rules', label: '分组转换' },
  { id: 'settings', label: '监测设置' },
  { id: 'logs', label: '日志' },
  { id: 'connection', label: '连接配置' },
]

export default function App() {
  const [auth, setAuth] = useState<AuthState | null>(null)
  const [tab, setTab] = useState<Tab>('overview')
  const [boot, setBoot] = useState<Bootstrap | null>(null)
  const toast = useToast()

  const [authError, setAuthError] = useState('')

  // Which edges of the tab strip are actually hiding content, so index.css can
  // fade exactly those. Without it the strip scrolls invisibly: the 6th tab
  // measured x=360-432 in a 390px viewport, cut off with no scrollbar and no
  // hint that it was there.
  //
  // The element is held in state rather than a ref so the effect below runs
  // when the strip actually mounts. The strip is conditionally rendered (only
  // once `authenticated` is true), and a ref-based effect that early-returns on
  // a null ref would never re-run: its dependency is `authenticated`, which
  // does not change again, so `data-scroll` would stay at its initial 'none'
  // and the fade would never appear.
  const [tabStripEl, setTabStripEl] = useState<HTMLDivElement | null>(null)
  const [tabScroll, setTabScroll] = useState<'none' | 'start' | 'middle' | 'end'>('none')

  useEffect(() => {
    if (!tabStripEl) return
    const el = tabStripEl
    // Guards the one path that outlives the effect: `fonts.ready` may resolve
    // after unmount, and the update would then set state on a dead component.
    // React tolerates it today (no warning, no throw), but the cleanup below
    // removes every other listener, so this is the odd one out.
    let alive = true
    const update = () => {
      if (!alive) return
      const max = el.scrollWidth - el.clientWidth
      if (max <= 1) setTabScroll('none')
      else if (el.scrollLeft <= 1) setTabScroll('start')
      else if (el.scrollLeft >= max - 1) setTabScroll('end')
      else setTabScroll('middle')
    }
    update()
    el.addEventListener('scroll', update, { passive: true })
    window.addEventListener('resize', update)
    // A ResizeObserver on the strip alone is not enough: the strip's own box is
    // set by its parent, so it does not fire when the *labels* get wider inside
    // it — which is exactly what happens when the CJK fallback font lands after
    // first paint (measured: scrollWidth 358 -> 416 with clientWidth unchanged,
    // so the strip silently started clipping the last tab while `data-scroll`
    // still said "nothing to scroll"). Watching the tabs themselves covers it,
    // and `fonts.ready` covers the swap that happens before they mount.
    //
    // The strip itself is observed too. Its own resize only reaches us through
    // the `resize` listener above, so a container that changes width without
    // the window doing so (a zoom, a devtools dock, an embedded frame) would
    // leave a stale fade. Observing both costs nothing and the two paths are
    // redundant on purpose: whichever fires first recomputes the same answer.
    const ro = new ResizeObserver(update)
    ro.observe(el)
    for (const child of el.children) ro.observe(child)
    document.fonts?.ready.then(update).catch(() => {})
    return () => {
      alive = false
      el.removeEventListener('scroll', update)
      window.removeEventListener('resize', update)
      ro.disconnect()
    }
  }, [tabStripEl])

  const checkAuth = useCallback(async () => {
    try {
      setAuth(await api.authState())
      setAuthError('')
    } catch (e) {
      // The auth error cannot be a toast: this runs before the panel (and its
      // toast host) is on screen, and the message has to stay put until the
      // operator retries — a toast would expire while they read it.
      setAuthError(e instanceof Error ? e.message : String(e))
    }
  }, [])

  useEffect(() => {
    const expire = () => {
      setBoot(null)
      setAuth({ needs_setup: false, authenticated: false, username: '' })
    }
    window.addEventListener(SESSION_EXPIRED, expire)
    return () => window.removeEventListener(SESSION_EXPIRED, expire)
  }, [])

  const refresh = useCallback(async () => {
    try {
      setBoot(await api.bootstrap())
    } catch (e) {
      const msg = e instanceof Error ? e.message : String(e)
      // A 401 means the session expired or was revoked: drop back to the login
      // screen rather than leaving a dead panel showing zeros.
      if (msg.includes('未登录') || msg.includes('401')) {
        setAuth({ needs_setup: false, authenticated: false, username: '' })
        return
      }
      toast.error(msg)
    }
  }, [toast])

  useEffect(() => {
    void checkAuth()
  }, [checkAuth])

  useEffect(() => {
    if (auth?.authenticated) void refresh()
  }, [auth, refresh])

  // Action outcomes are toasts: they follow the operator across a tab change
  // and do not push the page content around while it is being read.
  const flash = useCallback((msg: string) => toast.success(msg), [toast])
  const fail = useCallback((e: unknown) => toast.error(e instanceof Error ? e.message : String(e)), [toast])

  if (!auth) {
    return (
      <div className="flex min-h-full items-center justify-center p-4">
        <div className="w-full max-w-sm text-center">
          {authError ? (
            <>
              <ErrorNote>{authError}</ErrorNote>
              <Button kind="primary" onClick={() => void checkAuth()}>
                重试
              </Button>
            </>
          ) : (
            <span className="text-xs text-muted-foreground">载入中…</span>
          )}
        </div>
      </div>
    )
  }

  if (!auth.authenticated) {
    return <Login needsSetup={auth.needs_setup} onDone={checkAuth} />
  }

  const logout = async () => {
    try {
      await api.logout()
      await checkAuth()
    } catch (e) {
      fail(e)
    }
  }

  return (
    <div className="min-h-full">
      <header className="sticky top-0 z-20 border-b border-border bg-background/85 backdrop-blur-sm">
        <div className="mx-auto flex max-w-6xl items-center justify-between gap-3 px-4 py-3">
          <div className="flex min-w-0 items-center gap-2.5">
            <span
              aria-hidden="true"
              className="flex size-7 shrink-0 items-center justify-center rounded-control bg-primary text-primary-foreground"
            >
              <svg viewBox="0 0 16 16" className="size-3.5">
                <path
                  d="M2.5 4.5h11M2.5 8h7.5M2.5 11.5h9.5"
                  fill="none"
                  stroke="currentColor"
                  strokeWidth="1.75"
                  strokeLinecap="round"
                />
              </svg>
            </span>
            <div className="min-w-0">
              <h1 className="truncate text-sm font-semibold tracking-tight">QQBot 群监测</h1>
              <p className="hidden truncate text-2xs text-muted-foreground sm:block">
                QQ 群与 new-api 账号绑定
              </p>
            </div>
          </div>
          <div className="flex shrink-0 items-center gap-2">
            {boot && (
              <Badge tone={boot.configured ? 'ok' : 'warn'}>
                {boot.configured ? '已配置' : '未配置'}
              </Badge>
            )}
            <span className="hidden text-xs text-muted-foreground sm:inline">{auth.username}</span>
            <ThemeToggle />
            <Button kind="ghost" onClick={logout} title="退出登录">
              退出
            </Button>
          </div>
        </div>
        {/*
          The strip scrolls rather than wraps: six tabs wrapped to 81px each on a
          390px screen against 38px on desktop, which doubled the header for no
          extra information. The scrollbar is hidden, so the strip publishes its
          own scroll state and index.css draws the edge fade from it.
        */}
        <nav className="mx-auto max-w-6xl px-4">
          <div
            ref={setTabStripEl}
            data-scroll={tabScroll}
            className="tab-strip -mb-px flex gap-1 overflow-x-auto"
          >
            {TABS.map((t) => (
              <button
                key={t.id}
                type="button"
                aria-current={tab === t.id ? 'page' : undefined}
                onClick={() => setTab(t.id)}
                // A clipped tab is "partly visible", so Chrome decides no
                // scrolling is needed and Tab can land on a tab the operator
                // cannot see (measured: 连接配置 box x=360-432 in a 390px
                // viewport, scrollLeft stayed 0). `nearest` moves it the minimum
                // distance and does nothing when it is already in view.
                onFocus={(e) => e.currentTarget.scrollIntoView({ inline: 'nearest', block: 'nearest' })}
                className={`shrink-0 border-b-2 px-3 py-2 text-xs whitespace-nowrap transition-colors ${
                  tab === t.id
                    ? 'border-primary font-medium text-foreground'
                    : 'border-transparent text-muted-foreground hover:border-border hover:text-foreground'
                }`}
              >
                {t.label}
              </button>
            ))}
          </div>
        </nav>
      </header>

      <main className="mx-auto max-w-6xl space-y-4 p-4">
        {/* One tab's render error must not unmount the panel: the nav and tab
            bar are outside this boundary, so another tab is always reachable. */}
        <TabBoundary tab={tab}>
        {tab === 'overview' && <Overview boot={boot} onDone={flash} onFail={fail} onRefresh={refresh} />}
        {tab === 'groups' && <Groups onDone={flash} onFail={fail} onRefresh={refresh} />}
        {tab === 'users' && <Users onDone={flash} onFail={fail} onRefresh={refresh} />}
        {tab === 'rules' && <Rules onDone={flash} onFail={fail} />}
        {tab === 'settings' && <SettingsView onDone={flash} onFail={fail} onRefresh={refresh} />}
        {tab === 'logs' && <LogsView onFail={fail} />}
        {tab === 'connection' && (
          <Connection onDone={flash} onFail={fail} onRefresh={refresh} username={auth.username} />
        )}
        </TabBoundary>
      </main>
    </div>
  )
}

/* ---------------- overview ---------------- */

function Overview({
  boot,
  onDone,
  onFail,
  onRefresh,
}: {
  boot: Bootstrap | null
  onDone: (m: string) => void
  onFail: (e: unknown) => void
  onRefresh: () => Promise<void>
}) {
  const [busy, setBusy] = useState('')
  const [members, setMembers] = useState<MembersResponse | null>(null)
  const [manualOpen, setManualOpen] = useState(false)

  const load = useCallback(async () => {
    try {
      setMembers(await api.members())
    } catch (e) {
      onFail(e)
    }
  }, [onFail])

  useEffect(() => {
    void load()
  }, [load])

  const run = async (name: string, label: string) => {
    setBusy(name)
    try {
      const res = await api.action(name)
      onDone(`${label}完成：${JSON.stringify(res)}`)
      await load()
      await onRefresh()
    } catch (e) {
      onFail(e)
    } finally {
      setBusy('')
    }
  }

  const total = members?.total ?? 0
  const bound = members?.items.filter((m) => m.bound).length ?? 0
  const checked = members?.items.filter((m) => m.checked_today).length ?? 0
  const awarded = members?.items.filter((m) => m.reward_state === 'paid').length ?? 0
  const uncertain = members?.items.filter((m) => ['claimed', 'ambiguous', 'legacy'].includes(m.reward_state ?? '')).length ?? 0

  return (
    <>
      <Card title="状态">
        <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
          <Stat label="群成员" value={total} />
          <Stat label="已绑定" value={bound} />
          <Stat label="今日已签到" value={checked} />
          <Stat label="今日已发放" value={awarded} />
        </div>
        <p className="mt-4 flex flex-wrap items-center gap-x-2 gap-y-1 border-t border-border pt-3 text-xs text-muted-foreground">
          <span>统计日期 {members?.date ?? '-'}</span>
          <span aria-hidden="true">·</span>
          <span>监测群 {boot?.groups?.filter((g) => g.enabled).length ?? 0} 个</span>
          {uncertain > 0 && (
            <>
              <span aria-hidden="true">·</span>
              <span className="text-destructive">
                {uncertain} 笔奖励待确认（正在处理、结果不明或历史记录；不会自动重复发放。若长期未结算，请先核对网关记录）
              </span>
            </>
          )}
        </p>
      </Card>

      <Card
        title="手动执行"
        description="这些操作也会按各自的周期自动执行；这里的按钮用于立即触发一次。"
        actions={busy ? <Badge tone="accent">执行中…</Badge> : null}
      >
        <div className="flex flex-wrap gap-2">
          <Button kind="primary" disabled={!!busy} onClick={() => run('sync', '同步成员')}>
            同步成员
          </Button>
          <Button disabled={!!busy} onClick={() => run('bind', '自动绑定')}>
            自动绑定
          </Button>
          <Button disabled={!!busy} onClick={() => run('promote', '分组提升')}>
            分组提升
          </Button>
          <Button disabled={!!busy} onClick={() => run('checkin', '同步打卡')}>
            同步打卡
          </Button>
          <Button disabled={!!busy} onClick={() => run('award', '发放奖励')}>
            发放奖励
          </Button>
          <Button disabled={!!busy} onClick={() => setManualOpen(true)}>
            手动签到
          </Button>
        </div>
        <p className="mt-3 border-t border-border pt-3 text-2xs text-muted-foreground">
          「手动签到」用于验证「消息即签到」这条路：选一个成员，按当天记录一次签到（同一天只会记一次），随后可以点「发放奖励」看他是否拿到 quota。
        </p>
      </Card>

      {manualOpen && (
        <ManualCheckin
          members={members?.items ?? []}
          onClose={() => setManualOpen(false)}
          onDone={async (msg) => {
            setManualOpen(false)
            onDone(msg)
            await load()
            await onRefresh()
          }}
          onFail={onFail}
        />
      )}
    </>
  )
}

/**
 * ManualCheckin records a message-driven check-in for one member, so the path
 * can be exercised without waiting for somebody to speak in a group.
 *
 * It is the same service call the event listener makes, including the
 * once-per-day rule, so what the operator sees here is what a real message
 * would do.
 */
function ManualCheckin({
  members,
  onClose,
  onDone,
  onFail,
}: {
  members: Member[]
  onClose: () => void
  onDone: (m: string) => void
  onFail: (e: unknown) => void
}) {
  const [qq, setQQ] = useState('')
  const [groupID, setGroupID] = useState('')
  const [busy, setBusy] = useState(false)

  // Only members that are actually in a group can check in, and the group has to
  // be one they are in: a check-in is a message in a specific group.
  const options = members
    .filter((m) => m.qq > 0 && (m.group_ids?.length ?? 0) > 0)
    .map((m) => ({
      value: String(m.qq),
      label: `${m.qq}${m.nickname ? `（${m.nickname}）` : ''}`,
      keywords: m.nickname,
    }))
  const selected = members.find((m) => String(m.qq) === qq)
  const groupOptions = selected?.group_ids ?? []
  const effectiveGroup = groupID || String(groupOptions[0] ?? '')

  const problem = !qq ? '请选择群成员' : !effectiveGroup ? '该成员不在任何监测群里' : ''

  const run = async () => {
    if (problem) {
      onFail(new Error(problem))
      return
    }
    setBusy(true)
    try {
      const res = await api.action('checkin-member', {
        qq: Number(qq),
        group_id: Number(effectiveGroup),
      })
      onDone(
        res.recorded
          ? `已为 ${qq} 记录 ${res.date} 的签到`
          : res.unbound
            ? `${qq} 尚未绑定账号，绑定后才能签到`
            : `${qq} 在 ${res.date} 已经签过到（同一天只记一次）`,
      )
    } catch (e) {
      onFail(e)
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal
      busy={busy}
      title="手动记录签到"
      onClose={onClose}
      actions={
        <>
          <Button onClick={onClose} disabled={busy}>
            取消
          </Button>
          <Button kind="primary" onClick={run} disabled={busy || problem !== ''}>
            {busy ? '记录中…' : '记录签到'}
          </Button>
        </>
      }
    >
      <div className="space-y-4">
        <Field
          label="群成员"
          hint="只列出有群成员记录的成员；列表按「群管理」同步到的成员，含已关闭的群"
        >
          <Combobox
            value={qq}
            onChange={(v) => {
              setQQ(v)
              setGroupID('')
            }}
            options={options}
            ariaLabel="选择群成员"
            placeholder="搜索 QQ 号或昵称"
            emptyText="没有在群的成员；先到「群管理」同步成员"
            className="w-full"
          />
        </Field>
        {groupOptions.length > 1 && (
          <Field label="所在群" hint="该成员在多个群里，选择在哪一个群里签到（含已关闭的群）">
            <Select value={effectiveGroup} onChange={setGroupID} className="w-full">
              {groupOptions.map((g) => (
                <option key={g} value={String(g)}>
                  {g}
                </option>
              ))}
            </Select>
          </Field>
        )}
        {problem && <WarnNote>{problem}</WarnNote>}
      </div>
    </Modal>
  )
}

function Stat({ label, value }: { label: string; value: number }) {
  return (
    <div className="rounded-panel border border-border bg-background px-3 py-2.5">
      <div className="text-xs text-muted-foreground">{label}</div>
      <div className="mt-1 text-xl leading-7 font-semibold tabular-nums">{value}</div>
    </div>
  )
}

/* ---------------- groups ---------------- */

function Groups({
  onDone,
  onFail,
  onRefresh,
}: {
  onDone: (m: string) => void
  onFail: (e: unknown) => void
  onRefresh: () => Promise<void>
}) {
  const [groups, setGroups] = useState<Group[]>([])
  const [loaded, setLoaded] = useState(false)
  const [loadError, setLoadError] = useState('')
  const [selected, setSelected] = useState<Set<number>>(new Set())
  const [busy, setBusy] = useState(false)

  const load = useCallback(async () => {
    try {
      setLoaded(false)
      setLoadError('')
      const res = await api.groups()
      setGroups(res.items ?? [])
      setSelected(new Set((res.items ?? []).filter((g) => g.enabled).map((g) => g.group_id)))
      setLoaded(true)
    } catch (e) {
      setLoadError(e instanceof Error ? e.message : String(e))
      onFail(e)
    }
  }, [onFail])

  useEffect(() => {
    void load()
  }, [load])

  const refresh = async () => {
    setBusy(true)
    try {
      const gs = await api.refreshGroups()
      setGroups(gs ?? [])
      setSelected(new Set((gs ?? []).filter((g) => g.enabled).map((g) => g.group_id)))
      setLoaded(true)
      setLoadError('')
      onDone('群列表已从 QQ 刷新')
      await onRefresh()
    } catch (e) {
      onFail(e)
    } finally {
      setBusy(false)
    }
  }

  const save = async () => {
    if (!loaded || busy) return
    setBusy(true)
    try {
      const gs = await api.saveGroups([...selected])
      setGroups(gs ?? [])
      onDone('监测群已保存')
      await onRefresh()
    } catch (e) {
      onFail(e)
    } finally {
      setBusy(false)
    }
  }

  const toggle = (id: number) => {
    setSelected((prev) => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }

  return (
    <Card
      title="监测群"
      description="用开关选择要监测的群。新发现的群默认不监测，需要手动打开。"
      actions={
        <>
          <Button disabled={busy} onClick={refresh}>
            从 QQ 刷新
          </Button>
          <Button kind="primary" disabled={busy || !loaded} onClick={save}>
            保存
          </Button>
        </>
      }
    >
      {loadError && <><ErrorNote>{loadError}</ErrorNote><Button onClick={load}>重试</Button></>}
      {!loaded && !loadError ? <Empty>载入中…</Empty> : groups.length === 0 ? (
        <Empty>还没有群。点「从 QQ 刷新」读取机器人账号所在的群。</Empty>
      ) : (
        <Table head={['监测', '群号', '群名', '人数']}>
          {groups.map((g) => (
            <Row key={g.group_id}>
              <Cell>
                <Switch
                  checked={selected.has(g.group_id)}
                  onChange={() => toggle(g.group_id)}
                  label={selected.has(g.group_id) ? '已监测' : '未监测'}
                />
              </Cell>
              <Cell className="font-mono text-xs">{g.group_id}</Cell>
              <Cell>{g.group_name}</Cell>
              <Cell className="text-muted-foreground tabular-nums">{g.member_count}</Cell>
            </Row>
          ))}
        </Table>
      )}
    </Card>
  )
}

/* ---------------- users ---------------- */

function Users({
  onDone,
  onFail,
  onRefresh,
}: {
  onDone: (m: string) => void
  onFail: (e: unknown) => void
  onRefresh: () => Promise<void>
}) {
  const [data, setData] = useState<UsersResponse | null>(null)
  const [editing, setEditing] = useState<UserRow | null>(null)
  const [unbinding, setUnbinding] = useState<UserRow | null>(null)
  const [groups, setGroups] = useState<string[]>([])
  const [busy, setBusy] = useState(false)

  // Filters. `q` is debounced into `keyword` so typing does not fire a request
  // per keystroke.
  const [bound, setBound] = useState<'' | 'bound' | 'unbound'>('')
  const [inGroup, setInGroup] = useState<'' | 'in' | 'out'>('')
  const [kind, setKind] = useState<'' | 'member' | 'left' | 'account'>('')
  const [group, setGroup] = useState('')
  const [q, setQ] = useState('')
  const [keyword, setKeyword] = useState('')

  const [selected, setSelected] = useState<Set<number>>(new Set())
  const [batchGroup, setBatchGroup] = useState('')
  const groupWrite = useRef(false)
  // loadError is the directory's persistent failure state. A toast expires and
  // left the PREVIOUS rows on screen under the NEW filter value — 10001 (bound)
  // stayed listed while the control said 未绑定, so the table contradicted the
  // filter with nothing on the page to say why.
  const [loadError, setLoadError] = useState('')
  // batchResults keeps the per-account outcome on the page. The API reports
  // each account separately (ADR-0007) and the toast that used to be the only
  // carrier expired, so the detail was gone before it could be acted on.
  const [batchResults, setBatchResults] = useState<{ group: string; results: { user_id: number; ok: boolean; error?: string }[] } | null>(null)

  useEffect(() => {
    const t = window.setTimeout(() => setKeyword(q.trim()), 300)
    return () => window.clearTimeout(t)
  }, [q])

  const filter: UserFilter = { bound, in_group: inGroup, kind, group, q: keyword }
  const directoryGeneration = useRef(0)

  const load = useCallback(async () => {
    const generation = ++directoryGeneration.current
    try {
      const result = await api.users(filter)
      if (generation === directoryGeneration.current) {
        setData(result)
        setLoadError('')
      }
    } catch (e) {
      // A failed query must clear the rows, not keep them: the table would
      // otherwise show the previous filter's result under the new control
      // value, which reads as a wrong answer rather than a failed request.
      if (generation === directoryGeneration.current) {
        setData(null)
        setLoadError(e instanceof Error ? e.message : String(e))
        onFail(e)
      }
    }
    // filter is rebuilt each render; depend on its fields instead.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [bound, inGroup, kind, group, keyword, onFail])

  useEffect(() => {
    void load()
    return () => { directoryGeneration.current++ }
  }, [load])

  useEffect(() => {
    // Group names come from the gateway; a failure only costs the picker.
    api
      .newAPIGroups()
      .then((g) => setGroups(Object.keys(g).sort()))
      .catch(() => setGroups([]))
  }, [])

  // Drop selections for rows that a filter change removed from the view, so a
  // batch never touches something the operator can no longer see.
  useEffect(() => {
    const visible = new Set((data?.items ?? []).map((r) => r.newapi_user_id).filter(Boolean))
    setSelected((prev) => {
      const next = new Set<number>()
      for (const id of prev) if (visible.has(id)) next.add(id)
      return next
    })
  }, [data])

  const items = data?.items ?? []
  const selectable = [...new Set(items.flatMap((r) => r.newapi_user_id ? [r.newapi_user_id] : []))]

  const toggle = (id: number) =>
    setSelected((prev) => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })

  const toggleAll = () =>
    setSelected((prev) =>
      prev.size === selectable.length ? new Set() : new Set(selectable),
    )

  const sync = async () => {
    setBusy(true)
    try {
      const res = await api.syncUsers()
      onDone(`已从 new-api 同步 ${res.accounts} 个账号`)
      await load()
    } catch (e) {
      onFail(e)
    } finally {
      setBusy(false)
    }
  }

  const applyBatch = async () => {
    if (selected.size === 0 || groupWrite.current || busy) return
    if (!batchGroup) {
      onFail(new Error('请先选择目标分组'))
      return
    }
    groupWrite.current = true
    setBusy(true)
    try {
      const res = await api.setUserGroups([...selected], batchGroup)
      // The per-account results stay on the page: one role-protected account
      // must not hide the rest of the selection's outcome, and a toast that
      // expires cannot carry a list.
      const results = res.results ?? []
      setBatchResults({ group: batchGroup, results })
      // Derived from `results`, not from the response's counters: a 200 whose
      // body omits them printed "成功 undefined 个，失败 undefined 个".
      const failed = results.filter((r) => !r.ok).length
      const ok = results.length - failed
      if (failed === 0) {
        onDone(`已把 ${ok} 个账号调整到「${batchGroup}」`)
      } else {
        onDone(`成功 ${ok} 个，失败 ${failed} 个（明细见下表）`)
      }
      setSelected(new Set())
      await load()
      await onRefresh()
    } catch (e) {
      // Clear the previous batch's note: it described a DIFFERENT run, and
      // leaving it under this failure's toast read as if it belonged to it.
      setBatchResults(null)
      onFail(e)
    } finally {
      groupWrite.current = false
      setBusy(false)
    }
  }

  const resetFilters = () => {
    setBound('')
    setInGroup('')
    setKind('')
    setGroup('')
    setQ('')
  }

  const s = data?.summary

  return (
    <>
      <Card
        title="用户管理"
        actions={
          <>
            <Button disabled={busy} onClick={sync}>
              从 new-api 同步账号
            </Button>
            <Button disabled={busy} onClick={load}>
              刷新
            </Button>
          </>
        }
      >
        {loadError && (
          <div className="mb-3">
            <ErrorNote>目录读取失败：{loadError}。当前列表已清空，避免显示与筛选条件不符的旧数据。</ErrorNote>
          </div>
        )}
        {s && data && (
          <p className="mb-3 flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-muted-foreground">
            <span>共 {data.total} 行{data.total !== s.total && `（全部 ${s.total} 行）`}</span>
            <span aria-hidden="true">·</span>
            <span>在群 {s.in_group}</span>
            <span aria-hidden="true">·</span>
            <span>已退群 {s.left}</span>
            <span aria-hidden="true">·</span>
            <span>已绑定 {s.bound}</span>
            <span aria-hidden="true">·</span>
            <span>未绑定 {s.unbound}</span>
            <span aria-hidden="true">·</span>
            <span>未进群账号 {s.not_joined}</span>
          </p>
        )}

        {/*
          The filter grid and the clear button share one row. `items-end` put
          the button on its own line (measured y=301 against the grid's
          bottom=289) because the grid claims `basis-full` — so the one control
          that undoes the filters sat below the controls that set them.
        */}
        <div className="mb-4 grid gap-3 sm:grid-cols-2 lg:grid-cols-6">
          <Field label="绑定状态">
            <Select value={bound} onChange={(v) => setBound(v as typeof bound)} className="w-full">
              <option value="">全部</option>
              <option value="bound">已绑定</option>
              <option value="unbound">未绑定</option>
            </Select>
          </Field>
          <Field label="群状态">
            <Select value={inGroup} onChange={(v) => setInGroup(v as typeof inGroup)} className="w-full">
              <option value="">全部</option>
              <option value="in">在群</option>
              <option value="out">已退群 / 未进群</option>
            </Select>
          </Field>
          <Field label="分组">
            <Select value={group} onChange={setGroup} className="w-full">
              <option value="">全部分组</option>
              {groups.map((g) => (
                <option key={g} value={g}>
                  {g}
                </option>
              ))}
            </Select>
          </Field>
          <Field label="类型">
            <Select value={kind} onChange={(v) => setKind(v as typeof kind)} className="w-full">
              <option value="">全部</option>
              <option value="member">在群成员</option>
              <option value="left">已退群成员</option>
              <option value="account">未进群账号</option>
            </Select>
          </Field>
          <Field label="搜索" hint="QQ / 昵称 / new-api 用户名 / 邮箱">
            <Input value={q} onChange={setQ} placeholder="例如 54321 或 alice" />
          </Field>
          {/* Clearing sits with the controls that set the filters, not below the table. */}
          <FieldAction>
            <Button kind="ghost" onClick={resetFilters} disabled={busy} full>
              清除筛选
            </Button>
          </FieldAction>
        </div>

        {selected.size > 0 && (
          <div className="mb-3 flex flex-wrap items-center gap-2 rounded-panel border border-primary/40 bg-primary/5 px-3 py-2">
            <span className="text-xs font-medium">已选 {selected.size} 个账号</span>
            <Select value={batchGroup} onChange={setBatchGroup} ariaLabel="批量调整的目标分组" className="w-40">
              <option value="">选择目标分组…</option>
              {groups.map((g) => (
                <option key={g} value={g}>
                  {g}
                </option>
              ))}
            </Select>
            <Button kind="primary" disabled={busy || !batchGroup} onClick={applyBatch}>
              批量调整分组
            </Button>
            <Button kind="ghost" onClick={() => setSelected(new Set())}>
              取消选择
            </Button>
          </div>
        )}

        {/*
          The per-account outcome stays on the page. ADR-0007 makes each
          account's result its own report — one role-protected account must not
          hide the rest — and a toast expires before it can be read or copied.
        */}
        {batchResults && batchResults.results.length > 0 && (() => {
          // The container follows the OUTCOME: a fully successful batch is not
          // an error. Rendering it in ErrorNote (role="alert", destructive
          // colours) announced "成功 2 个，失败 0 个" as a failure and invited a
          // retry of a change that had already landed.
          const failed = batchResults.results.filter((r) => !r.ok)
          const okCount = batchResults.results.length - failed.length
          const body = (
            <>
              批量调整到「{batchResults.group}」的结果：成功 {okCount} 个，失败 {failed.length} 个
              {failed.length > 0 && (
                <ul className="mt-1 list-disc pl-4">
                  {failed.map((r) => (
                    <li key={r.user_id}>
                      #{r.user_id}：{r.error || '未知错误'}
                    </li>
                  ))}
                </ul>
              )}
            </>
          )
          return (
            <div className="mb-3">
              {failed.length > 0 ? (
                <ErrorNote onClose={() => setBatchResults(null)}>{body}</ErrorNote>
              ) : (
                <Note onClose={() => setBatchResults(null)}>{body}</Note>
              )}
            </div>
          )
        })()}

        {items.length === 0 ? (
          // A failed query clears the rows, so the empty state must not claim a
          // result for it: with the note above showing 目录读取失败, the page
          // also said 没有匹配的用户 and sent the operator to refresh/sync
          // members in 群管理 — an action that cannot fix a failed read.
          loadError ? null : (
            <Empty>
              没有匹配的用户。先到「群管理」刷新并同步成员；要看到没人进群的 new-api 账号，请点「从
              new-api 同步账号」。
            </Empty>
          )
        ) : (
          <Table
            stickyEnd={1}
            head={[
              <label
                key="all"
                className="flex size-6 cursor-pointer items-center justify-center"
                title="全选可调整的账号"
              >
                <input
                  type="checkbox"
                  className="size-4"
                  checked={selectable.length > 0 && selected.size === selectable.length}
                  onChange={toggleAll}
                  aria-label="全选可调整的账号"
                />
              </label>,
              'QQ',
              '昵称',
              'new-api 账号',
              '分组',
              '绑定方式',
              '今日已签到',
              '累计天数',
              '今日奖励（quota）',
              '操作',
            ]}
          >
            {items.map((m) => (
              <Row key={`${m.kind}-${m.qq}-${m.newapi_user_id ?? 0}`}>
                <Cell>
                  {m.newapi_user_id ? (
                    // The label carries the hit area: a bare 14px checkbox is well
                    // under the 24x24 minimum target size on a touch screen.
                    <label className="flex size-6 cursor-pointer items-center justify-center">
                      <input
                        type="checkbox"
                        className="size-4"
                        checked={selected.has(m.newapi_user_id)}
                        onChange={() => toggle(m.newapi_user_id!)}
                        aria-label={`选择账号 #${m.newapi_user_id}`}
                      />
                    </label>
                  ) : (
                    <span className="text-muted-foreground">—</span>
                  )}
                </Cell>
                <Cell className="font-mono text-xs">{m.qq || '—'}</Cell>
                <Cell>
                  <span className="inline-flex flex-wrap items-center gap-1.5">
                    {m.nickname || '—'}
                    {m.kind === 'account' && <Badge tone="info">未进群</Badge>}
                    {m.kind === 'left' && <Badge tone="warn">已退群</Badge>}
                  </span>
                </Cell>
                <Cell>
                  {m.bound || m.kind === 'account' ? (
                    <span className="inline-flex items-center gap-1.5">
                      {m.newapi_username || '—'}
                      <span className="text-xs text-muted-foreground">#{m.newapi_user_id}</span>
                    </span>
                  ) : (
                    <Badge tone="warn">未绑定</Badge>
                  )}
                </Cell>
                <Cell>
                  {m.newapi_user_id ? (
                    <Select
                      value={m.newapi_group ?? ''}
                      disabled={busy}
                      ariaLabel={`账号 #${m.newapi_user_id} 的分组`}
                      onChange={async (v) => {
                        if (!v || v === m.newapi_group || busy || groupWrite.current) return
                        groupWrite.current = true
                        setBusy(true)
                        try {
                          await api.setUserGroup(m.newapi_user_id!, v)
                          onDone(`已把 #${m.newapi_user_id} 调整到「${v}」`)
                          await load()
                          await onRefresh()
                        } catch (e) {
                          onFail(e)
                        } finally {
                          groupWrite.current = false
                          setBusy(false)
                        }
                      }}
                    >
                      <option value={m.newapi_group ?? ''}>{m.newapi_group || '—'}</option>
                      {groups
                        .filter((g) => g !== m.newapi_group)
                        .map((g) => (
                          <option key={g} value={g}>
                            {g}
                          </option>
                        ))}
                    </Select>
                  ) : (
                    <span className="text-muted-foreground">—</span>
                  )}
                </Cell>
                <Cell className="text-muted-foreground">{m.bind_source || '—'}</Cell>
                <Cell>
                  {m.checked_today ? (
                    <span className="inline-flex items-center gap-1.5">
                      <Badge tone="ok">已签到</Badge>
                      {/* Which source recorded it: the board (QQ's own record)
                          or a message. Worth showing because the two have
                          different failure modes when a patrol is off. */}
                      {m.checkin_source === 'message' && <Badge tone="info">消息</Badge>}
                      {m.checkin_source === 'board' && <Badge>打卡榜</Badge>}
                    </span>
                  ) : (
                    <Badge>未签到</Badge>
                  )}
                </Cell>
                <Cell className="tabular-nums">{m.checkin_days}</Cell>
                <Cell className="tabular-nums">
                  {['claimed', 'ambiguous', 'legacy'].includes(m.reward_state ?? '') ? (
                    <span className="text-destructive" title={m.reward_error || undefined}>
                      {m.reward_today || 0}（{m.reward_state === 'claimed' ? '处理中／待确认' : m.reward_state === 'legacy' ? '历史记录待核对' : '待核对'}）
                    </span>
                  ) : m.reward_state === 'paid' ? (
                    m.reward_today || '—'
                  ) : (
                    m.reward_today || '—'
                  )}
                </Cell>
                <Cell sticky>
                  <div className="flex justify-end gap-1.5">
                    <Button kind="ghost" size="sm" onClick={() => setEditing(m)}>
                      {m.bound ? '换绑' : m.kind === 'account' ? '绑定 QQ' : '绑定'}
                    </Button>
                    {m.bound && (
                      <Button
                        kind="ghost"
                        size="sm"
                        className="text-destructive hover:bg-destructive/10 hover:text-destructive"
                        onClick={() => setUnbinding(m)}
                      >
                        解绑
                      </Button>
                    )}
                  </div>
                </Cell>
              </Row>
            ))}
          </Table>
        )}
      </Card>

      {editing && (
        <RebindDialog
          member={editing}
          onClose={() => setEditing(null)}
          onDone={async (msg) => {
            setEditing(null)
            onDone(msg)
            await load()
            await onRefresh()
          }}
          onFail={onFail}
        />
      )}

      {unbinding && (
        <ConfirmDialog
          title="解绑"
          confirmLabel="解绑"
          message={
            <>
              将删除 QQ {unbinding.qq} 与账号「{unbinding.newapi_username || `#${unbinding.newapi_user_id ?? ''}`}」的绑定记录，且无法撤销；绑定方式也会一并丢失。
            </>
          }
          onConfirm={async () => {
            try {
              await api.deleteBinding(unbinding.qq)
              onDone(`已解绑 ${unbinding.qq}`)
              await load()
              await onRefresh()
            } catch (e) {
              onFail(e)
            } finally {
              setUnbinding(null)
            }
          }}
          onCancel={() => setUnbinding(null)}
        />
      )}
    </>
  )
}

/**
 * RebindDialog links a QQ and a new-api account.
 *
 * It works from either side, because the operator arrives from whichever row
 * showed the problem: an unbound member (pick the account) or an account nobody
 * has joined with (pick the QQ). Candidates come from the full directory, not
 * the table filter, so the operator selects real values instead of typing an
 * account id they would have to look up in new-api's own admin page.
 */
function RebindDialog({
  member,
  onClose,
  onDone,
  onFail,
}: {
  member: UserRow
  onClose: () => void
  onDone: (m: string) => void
  onFail: (e: unknown) => void
}) {
  // The row may be an account (no QQ yet) or a member (no account yet), so both
  // sides start from whatever it carries and are editable either way.
  const [qq, setQQ] = useState(member.qq ? String(member.qq) : '')
  const [accountID, setAccountID] = useState(member.newapi_user_id ? String(member.newapi_user_id) : '')
  const [busy, setBusy] = useState(false)
  const saving = useRef(false)
  const [members, setMembers] = useState<UserRow[]>([])
  const [loaded, setLoaded] = useState(false)
  const [loadError, setLoadError] = useState('')
  const [attempt, setAttempt] = useState(0)
  useEffect(() => {
    let active = true
    setLoaded(false)
    setLoadError('')
    api.users().then((res) => {
      if (active) { setMembers(res.items); setLoaded(true) }
    }).catch((e: unknown) => {
      if (active) setLoadError(e instanceof Error ? e.message : String(e))
    })
    return () => { active = false }
  }, [attempt])

  // QQ candidates: every known member/left row, plus the row itself. keywords
  // carry the nickname so the operator can search by name instead of the number
  // the label leads with.
  const qqOptions = members
    .filter((m) => m.qq > 0)
    .map((m) => ({ value: String(m.qq), label: `${m.qq}${m.nickname ? `（${m.nickname}）` : ''}`, keywords: m.nickname }))
  // Account candidates: every known account, plus the row itself. The current
  // selection is kept even if the cache does not list it, so an existing
  // binding can always be re-saved unchanged.
  const accountOptions = members
    .filter((m) => m.newapi_user_id)
    .map((m) => ({
      value: String(m.newapi_user_id),
      label: `${m.newapi_username || '—'} #${m.newapi_user_id}${m.newapi_email ? `（${m.newapi_email}）` : ''}`,
      keywords: `${m.newapi_username || ''} ${m.newapi_user_id} ${m.newapi_email || ''}`,
    }))
  const seenQQ = new Set(qqOptions.map((o) => o.value))
  if (member.qq > 0 && !seenQQ.has(String(member.qq))) {
    qqOptions.unshift({ value: String(member.qq), label: `${member.qq}（${member.nickname || '—'}）`, keywords: member.nickname })
  }
  const seenAcc = new Set(accountOptions.map((o) => o.value))
  if (member.newapi_user_id && !seenAcc.has(String(member.newapi_user_id))) {
    accountOptions.unshift({
      value: String(member.newapi_user_id),
      label: `${member.newapi_username || '—'} #${member.newapi_user_id}`,
      keywords: `${member.newapi_username || ''} ${member.newapi_user_id} ${member.newapi_email || ''}`,
    })
  }

  const qqNum = Number(qq)
  const accNum = Number(accountID)
  const problem =
    !Number.isInteger(qqNum) || qqNum <= 0
      ? '请选择 QQ'
      : !Number.isInteger(accNum) || accNum <= 0
        ? '请选择 new-api 账号'
        : ''

  const save = async () => {
    if (!loaded || saving.current) return
    if (problem) {
      onFail(new Error(problem))
      return
    }
    saving.current = true
    setBusy(true)
    try {
      const result = await api.createBinding({ qq: qqNum, newapi_user_id: accNum })
      onDone(`已把 QQ ${qqNum} 绑定到 new-api 账号 #${accNum}${result.rule_error ? `；分组转换失败：${result.rule_error}` : ''}`)
    } catch (e) {
      onFail(e)
    } finally {
      saving.current = false
      setBusy(false)
    }
  }

  return (
    <Modal
      busy={busy}
      title={member.bound ? `换绑 QQ ${member.qq || '—'}` : '绑定 QQ 与 new-api 账号'}
      onClose={onClose}
      actions={
        <>
          <Button onClick={onClose} disabled={busy}>取消</Button>
          <Button kind="primary" disabled={busy || !loaded || problem !== ''} onClick={save}>
            保存
          </Button>
        </>
      }
    >
      <div className="space-y-4">
        {!loaded && (loadError ? <><ErrorNote>{loadError}</ErrorNote><Button onClick={() => setAttempt((n) => n + 1)}>重试</Button></> : <p>正在读取绑定候选…</p>)}
        <Field label="QQ 号" hint="从群成员里选择">
          <Combobox disabled={busy || !loaded} value={qq} onChange={setQQ} options={qqOptions} ariaLabel="选择 QQ 号" placeholder="搜索 QQ 号或昵称" emptyText="没有匹配的群成员" className="w-full" />
        </Field>
        <Field label="new-api 账号" hint="从已同步的账号里选择；账号邮箱是 qq@qq.com 时通常可自动绑定">
          <Combobox disabled={busy || !loaded} value={accountID} onChange={setAccountID} options={accountOptions} ariaLabel="选择 new-api 账号" placeholder="搜索用户名、账号 ID 或邮箱" emptyText="没有匹配的账号" className="w-full" />
        </Field>
        {accountOptions.length <= 1 && (
          <p className="text-2xs text-muted-foreground">
            账号列表为空？先在上方点「同步 new-api 账号」把账号拉取下来。
          </p>
        )}
        {problem && <WarnNote>{problem}</WarnNote>}
      </div>
    </Modal>
  )
}

/* ---------------- connection ---------------- */

/**
 * CheckList renders the outcome of a connection test: one row per probed hop,
 * each with its own verdict, so a failure names the hop that is broken instead
 * of collapsing into a single "cannot connect".
 */
function CheckList({ results }: { results: CheckResult[] }) {
  return (
    <ul className="mt-3 space-y-1.5 border-t border-border pt-3">
      {results.map((r) => (
        <li key={r.name} className="flex items-start gap-2 text-xs">
          <span className="w-28 shrink-0 text-muted-foreground sm:w-32">{r.name}</span>
          {r.skipped ? (
            <Badge>跳过</Badge>
          ) : r.ok ? (
            <Badge tone="ok">成功</Badge>
          ) : (
            <Badge tone="err">失败</Badge>
          )}
          <span className={`min-w-0 break-all ${r.ok ? 'text-muted-foreground' : 'text-destructive'}`}>
            {r.error || r.detail}
          </span>
        </li>
      ))}
    </ul>
  )
}

function Connection({
  onDone,
  onFail,
  onRefresh,
  username,
}: {
  onDone: (m: string) => void
  onFail: (e: unknown) => void
  onRefresh: () => Promise<void>
  username: string
}) {
  const [c, setC] = useState<Config | null>(null)
  const [revs, setRevs] = useState<ConfigRevisions>({ newapi: '', napcat: '' })
  const [busy, setBusy] = useState('')
  const [autoOpen, setAutoOpen] = useState(false)
  const [newapiCheck, setNewapiCheck] = useState<CheckResult[] | null>(null)
  const [napcatCheck, setNapcatCheck] = useState<CheckResult[] | null>(null)
  const [cur, setCur] = useState('')
  const [next, setNext] = useState('')
  const [loadErr, setLoadErr] = useState('')

  const apply = useCallback((r: { config: Config; revisions: ConfigRevisions }) => {
    setC(r.config)
    setRevs(r.revisions)
    setLoadErr('')
  }, [])

  // saveSection response handler: adopt only the section that was saved. The
  // response carries the stored copy of the other half, and adopting it would
  // discard an unsaved edit the operator is still typing in that card.
  // isConflict recognizes the server's stale-revision refusal, which is the one
  // save failure where reloading is right (the form's values are known stale).
  const isConflict = (e: unknown) => {
    const msg = e instanceof Error ? e.message : String(e)
    return msg.includes('409') || msg.includes('已被修改')
  }

  const applySection = useCallback(
    (r: { config: Config; revisions: ConfigRevisions }, section: 'newapi' | 'napcat') => {
      setC((prev) =>
        prev
          ? section === 'newapi'
            ? { ...prev, newapi: r.config.newapi }
            : { ...prev, napcat: r.config.napcat }
          : r.config,
      )
      setRevs((prev) => ({ ...prev, [section]: r.revisions[section] }))
      setLoadErr('')
    },
    [],
  )

  const load = useCallback(() => {
    // Report the failure BOTH ways: a toast says what went wrong now, and the
    // inline error (below) keeps a retry on screen — a toast expires, and the
    // card would otherwise sit on 载入中… forever with no way forward.
    api
      .getConfig()
      .then((r) => apply(r))
      .catch((e) => {
        setLoadErr(e instanceof Error ? e.message : String(e))
        onFail(e)
      })
  }, [apply, onFail])

  useEffect(() => {
    load()
  }, [load])

  if (!c) {
    return (
      <Card title="连接配置">
        {loadErr ? (
          <div className="space-y-3">
            <ErrorNote>{loadErr}</ErrorNote>
            <Button size="sm" onClick={load}>
              重试
            </Button>
          </div>
        ) : (
          '载入中…'
        )}
      </Card>
    )
  }

  // Testing is a separate action from saving: the operator can check what they
  // typed before committing it, and a failed test never blocks a save.
  const testNewAPI = async () => {
    setBusy('test-newapi')
    setNewapiCheck(null)
    try {
      const r = await api.testNewAPI({
        url: c.newapi.url,
        username: c.newapi.username,
        password: c.newapi.password,
      })
      setNewapiCheck(r.results)
    } catch (e) {
      onFail(e)
    } finally {
      setBusy('')
    }
  }

  const testNapCat = async () => {
    setBusy('test-napcat')
    setNapcatCheck(null)
    try {
      const r = await api.testNapCat({
        webui_url: c.napcat.webui_url,
        webui_token: c.napcat.webui_token,
        onebot_url: c.napcat.onebot_url,
        onebot_token: c.napcat.onebot_token,
        event_ws_url: c.napcat.event_ws_url,
        event_ws_token: c.napcat.event_ws_token,
      })
      setNapcatCheck(r.results)
    } catch (e) {
      onFail(e)
    } finally {
      setBusy('')
    }
  }

  // The two halves save independently, so a half-filled NapCat form can never
  // block (or overwrite) the gateway, and vice versa.
  const saveSection = async (section: 'newapi' | 'napcat', label: string) => {
    setBusy(section)
    try {
      const r =
        section === 'newapi'
          ? await api.saveNewAPIConfig(c, revs.newapi)
          : await api.saveNapCatConfig(c, revs.napcat)
      applySection(r, section)
      onDone(`${label}已保存`)
      await onRefresh()
    } catch (e) {
      onFail(e)
      // A rejected save (a validation error, a stale revision) must leave every
      // unsaved edit in place so the operator can fix the one field that is
      // wrong; reloading here would silently discard the rest of the form.
      // The 409 path below reloads explicitly, where the values really are
      // stale and there is nothing to preserve.
      if (isConflict(e)) void load()
    } finally {
      setBusy('')
    }
  }

  return (
    <div className="space-y-4">
      <Card
        title="new-api 管理员账号"
        description="服务用这个账号登录 new-api 读取账号列表、调整分组与发放奖励。"
        actions={
          <>
            <Button disabled={busy !== ''} onClick={testNewAPI}>
              {busy === 'test-newapi' ? '测试中…' : '测试连接'}
            </Button>
            <Button kind="primary" disabled={busy !== ''} onClick={() => saveSection('newapi', 'new-api 连接配置')}>
              {busy === 'newapi' ? '保存中…' : '保存'}
            </Button>
          </>
        }
      >
        <div className="grid gap-4 sm:grid-cols-3">
          <Field label="URL" hint="例如 https://api.example.com">
            <Input
              value={c.newapi.url}
              onChange={(v) => setC({ ...c, newapi: { ...c.newapi, url: v } })}
              inputMode="text"
              autoComplete="url"
            />
          </Field>
          <Field label="账号">
            <Input
              value={c.newapi.username}
              onChange={(v) => setC({ ...c, newapi: { ...c.newapi, username: v } })}
              autoComplete="username"
            />
          </Field>
          <Field label="密码" hint="留空或保持 ******** 表示不修改">
            <Input
              type="password"
              value={c.newapi.password}
              onChange={(v) => setC({ ...c, newapi: { ...c.newapi, password: v } })}
              autoComplete="new-password"
            />
          </Field>
        </div>
        {newapiCheck && <CheckList results={newapiCheck} />}
      </Card>

      <Card
        title="NapCat"
        description="服务通过 NapCat 读取群成员与打卡榜、接收进群事件。"
        actions={
          <>
            <Button onClick={() => setAutoOpen(true)} disabled={busy !== ''}>
              自动配置
            </Button>
            <Button disabled={busy !== ''} onClick={testNapCat}>
              {busy === 'test-napcat' ? '测试中…' : '测试连接'}
            </Button>
            <Button kind="primary" disabled={busy !== ''} onClick={() => saveSection('napcat', 'NapCat 连接配置')}>
              {busy === 'napcat' ? '保存中…' : '保存'}
            </Button>
          </>
        }
      >
        <div className="mb-4 flex flex-wrap items-start gap-2 rounded-panel border border-info/40 bg-info/10 px-3 py-2.5 text-xs text-info">
          <span className="min-w-0 leading-5">
            只填 <b>WebUI 地址</b>与 <b>WebUI Token</b>，点「自动配置」即可：服务会登录 NapCat，
            自动开启 OneBot 的 HTTP 与 WebSocket 适配器，并把下面的 OneBot / 事件地址与 token 一并填好。
          </span>
        </div>
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="WebUI 地址" hint="浏览器里打开 NapCat 面板的地址，例如 http://127.0.0.1:6099">
            <Input
              value={c.napcat.webui_url}
              onChange={(v) => setC({ ...c, napcat: { ...c.napcat, webui_url: v } })}
            />
          </Field>
          <Field label="WebUI Token" hint="NapCat 面板的访问 Token；留空或保持 ******** 表示不修改">
            <Input
              type="password"
              value={c.napcat.webui_token}
              onChange={(v) => setC({ ...c, napcat: { ...c.napcat, webui_token: v } })}
              autoComplete="new-password"
            />
          </Field>
          <Field label="OneBot HTTP URL" hint="巡检调用动作走这里；自动配置会填好">
            <Input
              value={c.napcat.onebot_url}
              onChange={(v) => setC({ ...c, napcat: { ...c.napcat, onebot_url: v } })}
            />
          </Field>
          <Field label="OneBot Token" hint="留空或保持 ******** 表示不修改">
            <Input
              type="password"
              value={c.napcat.onebot_token}
              onChange={(v) => setC({ ...c, napcat: { ...c.napcat, onebot_token: v } })}
              autoComplete="new-password"
            />
          </Field>
          <Field label="事件 WebSocket URL（可选）" hint="接收新成员进群与绑定指令；留空也能工作，只是反应慢一些">
            <Input
              value={c.napcat.event_ws_url}
              onChange={(v) => setC({ ...c, napcat: { ...c.napcat, event_ws_url: v } })}
            />
          </Field>
          <Field label="事件 WebSocket Token">
            <Input
              type="password"
              value={c.napcat.event_ws_token}
              onChange={(v) => setC({ ...c, napcat: { ...c.napcat, event_ws_token: v } })}
              autoComplete="new-password"
            />
          </Field>
        </div>
        {napcatCheck && <CheckList results={napcatCheck} />}
      </Card>

      {autoOpen && (
        <AutoConfigureNapCat
          config={c}
          revision={revs.napcat}
          onClose={() => setAutoOpen(false)}
          onDone={(r) => {
            // Auto-configure only writes the NapCat section, so the gateway
            // card's unsaved draft must survive it.
            applySection(r, 'napcat')
            setAutoOpen(false)
            onDone('NapCat 已自动配置：OneBot 适配器已开启，连接信息已保存')
            onRefresh()
          }}
          onFail={(e) => {
            onFail(e)
            load()
          }}
        />
      )}

      {/* Lives here rather than under 监测设置: it is the panel's own account,
          the same subject as the two connection cards above it, and it was the
          only unrelated card in the settings tab. */}
      <Card title="修改密码" description={`当前管理账号：${username}`}>
        <div className="grid gap-4 sm:grid-cols-3">
          <Field label="当前密码">
            <Input type="password" value={cur} onChange={setCur} autoComplete="current-password" />
          </Field>
          <Field label="新密码" hint="至少 8 位">
            <Input type="password" value={next} onChange={setNext} autoComplete="new-password" />
          </Field>
          <FieldAction>
            <Button
              disabled={busy !== ''}
              onClick={async () => {
                setBusy('password')
                try {
                  await api.changePassword(cur, next)
                  setCur('')
                  setNext('')
                  onDone('密码已修改')
                } catch (e) {
                  onFail(e)
                } finally {
                  setBusy('')
                }
              }}
            >
              {busy === 'password' ? '修改中…' : '修改密码'}
            </Button>
          </FieldAction>
        </div>
      </Card>
    </div>
  )
}

/**
 * AutoConfigureNapCat asks only for the WebUI address (and token when it is not
 * already stored), then lets the server write NapCat's OneBot adapters. The
 * advanced ports are optional and default to the ones the deployment notes use.
 */
function AutoConfigureNapCat({
  config,
  revision,
  onClose,
  onDone,
  onFail,
}: {
  config: Config
  revision: string
  onClose: () => void
  onDone: (r: { config: Config; revisions: ConfigRevisions }) => void
  onFail: (e: unknown) => void
}) {
  const [url, setURL] = useState(config.napcat.webui_url)
  const [token, setToken] = useState('')
  const [httpPort, setHTTPPort] = useState('3002')
  const [wsPort, setWSPort] = useState('3001')
  // Empty means the server's default (127.0.0.1). A containerised deployment
  // must set 0.0.0.0 here, because the container can never reach the host's
  // loopback and an adapter bound to 127.0.0.1 would be unreachable.
  const [bindHost, setBindHost] = useState('')
  const [showAdvanced, setShowAdvanced] = useState(false)
  const [busy, setBusy] = useState(false)

  const run = async () => {
    setBusy(true)
    try {
      const r = await api.autoConfigureNapCat({
        webui_url: url,
        webui_token: token,
        revision,
        http_port: Number(httpPort) || 0,
        ws_port: Number(wsPort) || 0,
        bind_host: bindHost.trim(),
      })
      onDone(r)
    } catch (e) {
      onFail(e)
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal
      busy={busy}
      title="自动配置 NapCat"
      onClose={onClose}
      actions={
        <>
          <Button onClick={onClose} disabled={busy}>
            取消
          </Button>
          <Button kind="primary" onClick={run} disabled={busy || url.trim() === ''}>
            {busy ? '配置中…' : '开始配置'}
          </Button>
        </>
      }
    >
      <div className="space-y-4">
        <p className="text-xs leading-5 text-muted-foreground">
          服务会用它登录 NapCat，开启 OneBot 的 HTTP 与 WebSocket 适配器（无需重启），并把得到的地址与
          token 写回「连接配置」。
        </p>
        <Field label="WebUI 地址">
          <Input value={url} onChange={setURL} placeholder="http://127.0.0.1:6099" />
        </Field>
        <Field
          label="WebUI Token"
          hint={config.napcat.webui_token ? '留空则沿用已保存的 Token' : '首次配置必填'}
        >
          <Input type="password" value={token} onChange={setToken} autoComplete="new-password" />
        </Field>
        <div>
          <Button kind="ghost" size="sm" onClick={() => setShowAdvanced((v) => !v)}>
            {showAdvanced ? '收起高级选项' : '高级选项（适配器端口）'}
          </Button>
        </div>
        {showAdvanced && (
          <Fieldset legend="适配器端口" hint="不确定就留默认值；服务与 NapCat 同机时用默认端口即可。">
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label="OneBot HTTP 端口">
                <Input value={httpPort} onChange={setHTTPPort} inputMode="numeric" />
              </Field>
              <Field label="OneBot WebSocket 端口">
                <Input value={wsPort} onChange={setWSPort} inputMode="numeric" />
              </Field>
            </div>
            <Field
              label="适配器监听地址"
              hint="留空 = 127.0.0.1（本服务与 NapCat 同机）。Docker 部署必须填 0.0.0.0：容器访问不到宿主的 127.0.0.1。"
            >
              <Input value={bindHost} onChange={setBindHost} placeholder="0.0.0.0" />
            </Field>
          </Fieldset>
        )}
      </div>
    </Modal>
  )
}
