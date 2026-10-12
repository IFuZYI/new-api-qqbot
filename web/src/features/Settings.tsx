/**
 * SettingsView is the 监测设置 tab: every automated path, its switch, and its
 * parameters in one place.
 *
 * The page is split by how a path is triggered, because that is the choice the
 * operator is making: 事件驱动 paths react to what happens in the group (a
 * command, a join, a message), while 巡检 paths poll on a timer and are the ones
 * that must keep running when nobody is talking. Every patrol's configuration —
 * switch, interval and health — lives in one card, so "what runs on a timer, and
 * is it healthy?" is answered in one glance instead of three cards scattered
 * between the event features.
 */
import { useCallback, useEffect, useState } from 'react'
import { api, type PatrolState, type Settings } from '../api'
import { Badge, Button, Card, ErrorNote, Field, Input, Select, Switch } from '../ui'
import { CommandList, FeatureCard } from '../features/common'
import { formatDateTime } from '../format'

/**
 * DEFAULT_CHECKIN_PATTERNS mirrors the server's shipped list, so switching from
 * 任意消息 back to 按指令 restores a usable list rather than an empty one that
 * would silently check nobody in.
 */
const DEFAULT_CHECKIN_PATTERNS = '/签到\n<签到>\n>>签到<<'

/** One patrol's row: what it does, how often, and how it is health-reported. */
type PatrolSpec = {
  /** Key in the scheduler's status map. */
  key: string
  label: string
  hint: string
  /**
   * Settings keys for the switch and the interval. Omitted for a patrol that is
   * configured elsewhere: 分组转换 runs per rule (see the 分组转换 tab), so this
   * card only reports its health.
   */
  enabledKey?: 'member_patrol_enabled' | 'bind_patrol_enabled' | 'checkin_patrol_enabled'
  minutesKey?: 'member_sync_minutes' | 'bind_patrol_minutes' | 'checkin_sync_minutes'
  /**
   * zeroPauses marks a patrol whose interval of 0 means "keep the switch, but
   * do not run on a timer" (the server preserves the 0). Only 绑定巡检 has that
   * meaning: the other intervals are repaired to their default when 0, and the
   * check-in loop in particular must keep running while either check-in path is
   * on — it is what pays the rewards, so a 0 there cannot be allowed to stop it.
   */
  zeroPauses?: boolean
}

/** Patrols in the order the scheduler runs them. */
const PATROLS: PatrolSpec[] = [
  {
    key: 'members',
    label: '群成员巡检',
    hint: '定期重读群成员列表，补齐昵称、群等级等资料。新成员进群与绑定成功都会立即记录，关掉只是资料更新变慢。',
    enabledKey: 'member_patrol_enabled',
    minutesKey: 'member_sync_minutes',
  },
  {
    key: 'bind',
    label: '绑定巡检',
    hint: '周期性重试所有未绑定的成员，补上「先进群、后注册」的账号。只处理未绑定的成员，不会改动已有绑定。间隔填 0 = 暂停定时（保留开关）。',
    enabledKey: 'bind_patrol_enabled',
    minutesKey: 'bind_patrol_minutes',
    zeroPauses: true,
  },
  {
    key: 'checkins',
    label: '打卡榜巡检',
    hint: '读取 QQ 群自带的打卡榜，补全排名与打卡时间。打卡没有事件通知，只能轮询；关掉只是不再从打卡榜记录签到，消息签到与奖励发放不受影响。',
    enabledKey: 'checkin_patrol_enabled',
    minutesKey: 'checkin_sync_minutes',
  },
  {
    key: 'rules',
    label: '分组转换巡检',
    hint: '每条规则按自己的周期运行（在「分组转换」页配置），这里只显示运行状态。',
  },
]

export default function SettingsView({
  onDone,
  onFail,
  onRefresh,
}: {
  onDone: (m: string) => void
  onFail: (e: unknown) => void
  onRefresh: () => Promise<void>
}) {
  const [s, setS] = useState<Settings | null>(null)
  const [anyMessage, setAnyMessage] = useState(false)
  const [patrols, setPatrols] = useState<Record<string, PatrolState>>({})
  const [busy, setBusy] = useState(false)
  const [loadErr, setLoadErr] = useState('')
  // Which patrol is being tested right now, so only its own button spins.
  const [testing, setTesting] = useState('')
  // The last manual test's outcome per patrol, kept until the next test: the
  // health badge says when it last ran, but not what the run found.
  const [tested, setTested] = useState<Record<string, string>>({})

  const load = useCallback(async () => {
    try {
      const r = await api.status()
      setS(r.settings)
      setAnyMessage(r.settings.checkin_patterns.trim() === '')
      setPatrols(r.patrols ?? {})
      setLoadErr('')
    } catch (e) {
      setLoadErr(e instanceof Error ? e.message : String(e))
      onFail(e)
    }
  }, [onFail])

  // refreshHealth re-reads only the patrol health, leaving the staged form
  // alone. 测试一次 must not call load(): the button exists so the operator can
  // try a patrol *before* saving the switch they just toggled, and load()'s
  // setS(server copy) would silently throw those unsaved edits away.
  const refreshHealth = useCallback(async () => {
    try {
      const r = await api.status()
      setPatrols(r.patrols ?? {})
    } catch (e) {
      onFail(e)
    }
  }, [onFail])

  useEffect(() => {
    void load()
  }, [load])

  if (!s) {
    return (
      <Card title="监测设置">
        {loadErr ? (
          <div className="space-y-3">
            <ErrorNote>{loadErr}</ErrorNote>
            <Button size="sm" onClick={() => void load()}>
              重试
            </Button>
          </div>
        ) : (
          '载入中…'
        )}
      </Card>
    )
  }

  const num = (k: keyof Settings) => (v: string) => setS({ ...s, [k]: Number(v) } as Settings)
  const str = (k: keyof Settings) => (v: string) => setS({ ...s, [k]: v } as Settings)
  const bool = (k: keyof Settings) => (v: boolean) => setS({ ...s, [k]: v } as Settings)
  // "Any message counts" is expressed as an empty pattern list, which is how the
  // server reads it too: there is then no message that fails to match.
  const emptyCommand = !anyMessage && !s.checkin_patterns.trim()

  const save = async () => {
    if (busy || emptyCommand) return
    setBusy(true)
    try {
      const saved = await api.saveSettings({ ...s, checkin_patterns: anyMessage ? '' : s.checkin_patterns })
      setS({ ...saved, checkin_patterns: s.checkin_patterns })
      onDone('设置已保存')
      await onRefresh()
      await refreshHealth()
    } catch (e) {
      onFail(e)
      // Keep the draft so validation failures can be corrected in place.
    } finally {
      setBusy(false)
    }
  }

  /**
   * Run one patrol now. The outcome is summarised in the row rather than only
   * toasted: the operator is testing this specific patrol, and a toast that
   * scrolls away leaves them guessing which run it belonged to.
   */
  const test = async (spec: PatrolSpec) => {
    setTesting(spec.key)
    try {
      const r = await api.runPatrol(spec.key)
      setTested((t) => ({ ...t, [spec.key]: summarise(spec, r.result) }))
      onDone(`${spec.label}：测试完成`)
      // Health only: the operator may be mid-edit, and the test is the one
      // thing they do before saving.
      await refreshHealth()
    } catch (e) {
      setTested((t) => ({ ...t, [spec.key]: e instanceof Error ? e.message : String(e) }))
      onFail(e)
      await refreshHealth()
    } finally {
      setTesting('')
    }
  }

  return (
    <div className="space-y-4">
      <Card
        title="监测设置"
        description="每一类自动化都有独立的开关；关掉的功能不会产生任何请求。保存后立即生效。"
        actions={
          <Button kind="primary" disabled={busy || emptyCommand} onClick={save}>
            {busy ? '保存中…' : '保存'}
          </Button>
        }
      >
        <p className="text-xs leading-5 text-muted-foreground">
          按触发方式分成两组：<strong className="font-medium text-foreground/80">事件驱动</strong>
          的路径由群里的动静触发（指令、进群、发言），反应最快；
          <strong className="font-medium text-foreground/80">定期巡检</strong>
          按周期轮询，是「没人说话时也要继续工作」的那部分。
        </p>
      </Card>

      {/* ---- 事件驱动 ---- */}
      <FeatureCard
        title="绑定指令"
        description="成员在群里发送指令，把 QQ 邮箱对应的 new-api 账号绑定到自己。只对未绑定的成员生效。"
        enabled={s.bind_command_enabled}
        onToggle={bool('bind_command_enabled')}
        badge={<Badge tone="info">事件驱动</Badge>}
      >
        <CommandList
          value={s.bind_command_patterns}
          onChange={str('bind_command_patterns')}
        />
      </FeatureCard>

      <FeatureCard
        title="进群自动绑定"
        description="成员加入监测群时立即按 QQ 邮箱匹配并绑定。"
        enabled={s.bind_on_join_enabled}
        onToggle={bool('bind_on_join_enabled')}
        badge={<Badge tone="info">事件驱动</Badge>}
      />

      <FeatureCard
        title="消息签到"
        description="把群里的消息当作当天签到。每天每人只记一次，重复消息不会重复发奖；未绑定账号的成员先绑定才能签到。"
        enabled={s.checkin_by_message_enabled}
        onToggle={bool('checkin_by_message_enabled')}
        badge={<Badge tone="info">事件驱动</Badge>}
      >
        <Field
          label="触发方式"
          hint="「任意消息」= 群里任何人说话都算签到；「按指令」= 只有下面列出的指令算。"
        >
          <Select
            value={anyMessage ? 'any' : 'command'}
            onChange={(v) => {
              setAnyMessage(v === 'any')
              if (v === 'command' && !s.checkin_patterns) setS({ ...s, checkin_patterns: DEFAULT_CHECKIN_PATTERNS })
            }}
            ariaLabel="签到触发方式"
            className="w-56"
          >
            <option value="any">任意消息</option>
            <option value="command">按指令</option>
          </Select>
        </Field>
        {emptyCommand && <ErrorNote>请填写至少一条签到指令，或明确选择「任意消息」。</ErrorNote>}
        {anyMessage ? (
          <p className="text-2xs text-muted-foreground">
            当前配置下，群里的任意一条消息都会为发送者记录当天签到。
          </p>
        ) : (
          <CommandList
            value={s.checkin_patterns}
            onChange={str('checkin_patterns')}
          />
        )}
      </FeatureCard>

      {/* ---- 定期巡检：所有巡检的开关、周期与状态都在一起 ---- */}
      <Card
        title="定期巡检"
        description="轮询是唯一真相来源：事件流只加速反应，不承担正确性。间隔以分钟为单位，最小 1；只有绑定巡检允许填 0（暂停定时、保留开关）。"
      >
        <div className="divide-y divide-border">
          {PATROLS.map((spec) => {
            const on = spec.enabledKey ? s[spec.enabledKey] : true
            const p = patrols[spec.key]
            return (
              // One grid for every row, so the interval, the test button and the
              // switch hold the same columns on all four. Laid out with flex,
              // the last row (分组转换巡检) has no interval field, so its 测试一次
              // button slid right to x=1190 while the other three sat at x=1094
              // — the action column moved between rows.
              //
              // The three columns only exist from `sm` up. `1fr auto auto` on a
              // 320px screen resolved the description to 0px and pushed the
              // controls to x=331 (measured: the whole page scrolled sideways by
              // 11px), because two `auto` columns take their content width first
              // and there was nothing left. Below `sm` the row stacks instead.
              <div
                key={spec.key}
                className="grid grid-cols-1 items-start gap-x-4 gap-y-3 py-3.5 first:pt-0 last:pb-0 sm:grid-cols-[1fr_7rem_10rem]"
              >
                <div className="min-w-0">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="text-sm font-medium">{spec.label}</span>
                    <PatrolHealth state={p} on={on} />
                  </div>
                  <p className="mt-1 text-2xs text-muted-foreground">
                    {spec.hint}
                    {on && p?.last_run && <> · 上次运行 {formatDateTime(p.last_run)}</>}
                  </p>
                  {on && p?.last_error && (
                    <p className="mt-1 text-2xs break-all text-destructive">{p.last_error}</p>
                  )}
                  {tested[spec.key] && (
                    <p className="mt-1 text-2xs break-all text-foreground/80">
                      测试结果：{tested[spec.key]}
                    </p>
                  )}
                </div>

                {/*
                  The third column is a fixed 10rem, and the row without an
                  interval still renders an element to occupy that cell. What
                  holds 测试一次 and its switch at one x across all four rows is
                  that occupancy, not the spacer's own width: measured, the
                  spacer can be narrowed to 10px or widened to 200px with the
                  buttons staying at x=1095.5, but deleting it drops that row's
                  buttons to x=967.5 (the actions div falls back into the
                  interval column). The numbers this replaced — the last row's
                  controls at x=1135 against 1102 on the other three — came from
                  an `auto` third column, which sizes per row: 121px on the row
                  whose actions are a bare button, 154px on the rows carrying a
                  switch, so the column started at a different x on each row.
                */}
                <div className="flex h-8 w-28 items-center gap-2" aria-hidden={!spec.minutesKey}>
                  {spec.minutesKey && (
                    <>
                      <Input
                        type="number"
                        min={spec.zeroPauses ? 0 : 1}
                        value={s[spec.minutesKey]}
                        onChange={(v) => {
                          // The server repairs a 0 on every patrol except 绑定巡检,
                          // so letting one be typed here would be silently undone
                          // on the next read. Clamp at the input instead: a
                          // near-miss becomes the smallest legal value rather than
                          // a number the server quietly rewrites.
                          const n = Number(v)
                          const floor = spec.zeroPauses ? 0 : 1
                          const next = Number.isFinite(n) ? Math.max(floor, n) : floor
                          setS({ ...s, [spec.minutesKey as string]: next } as Settings)
                        }}
                        ariaLabel={`${spec.label}间隔（分钟）`}
                        className="w-20"
                      />
                      <span className="text-xs whitespace-nowrap text-muted-foreground">分钟</span>
                    </>
                  )}
                </div>

                <div className="flex items-center gap-2">
                  <Button
                    size="sm"
                    disabled={testing !== ''}
                    title={`立即执行一次${spec.label}，用于在打开开关前先试一次`}
                    onClick={() => void test(spec)}
                  >
                    {testing === spec.key ? '测试中…' : '测试一次'}
                  </Button>

                  {spec.enabledKey ? (
                    <Switch
                      checked={on}
                      onChange={(v) => setS({ ...s, [spec.enabledKey as string]: v } as Settings)}
                      label={on ? '已启用' : '已停用'}
                      ariaLabel={`${spec.label}：${on ? '已启用' : '已停用'}`}
                    />
                  ) : (
                    <Badge>按规则</Badge>
                  )}
                </div>
              </div>
            )
          })}
        </div>
      </Card>

      <Card
        title="日志"
        description="服务把每一条记录同时写到按天切分的文件和进程控制台（docker logs / journalctl）。等级切换立即生效，无需重启。"
      >
        <div className="grid gap-x-6 gap-y-5 sm:grid-cols-2">
          <Field
            label="日志等级"
            hint="INFO 记录状态变化与失败；DEBUG 额外记录每一次请求与事件，排查问题时才打开"
          >
            <Select
              value={s.log_level}
              onChange={(v) => setS({ ...s, log_level: v as Settings['log_level'] })}
              ariaLabel="日志等级"
              className="w-56"
            >
              <option value="info">INFO（默认）</option>
              <option value="debug">DEBUG（详细）</option>
            </Select>
          </Field>
          <Field label="保留天数" hint="含今天；0 = 永久保留。超出的旧文件由服务每天自动删除">
            <div className="flex items-center gap-2">
              <Input
                type="number"
                min={0}
                max={3650}
                value={s.log_retention_days}
                onChange={(v) => {
                  const n = Number(v)
                  setS({ ...s, log_retention_days: Number.isFinite(n) ? Math.max(0, n) : 0 })
                }}
                ariaLabel="日志保留天数"
                className="w-24"
              />
              <span className="text-xs whitespace-nowrap text-muted-foreground">天</span>
            </div>
          </Field>
        </div>
        <p className="text-2xs text-muted-foreground">
          记录内容在「日志」页查看；保留天数只影响磁盘上的旧文件，不改变记录的内容。
        </p>
      </Card>

      <FeatureCard
        title="签到奖励"
        description="签到成功后按区间随机发放额度，单位是 new-api 的 quota；同一天同一人只发一次。"
        badge={<Badge>始终启用</Badge>}
      >
        <div className="grid gap-x-6 gap-y-5 sm:grid-cols-3">
          <Field label="奖励下限（quota）" hint="单位是 new-api 的 quota，不做货币换算">
            <Input type="number" min={0} value={s.reward_min} onChange={num('reward_min')} className="w-40" />
          </Field>
          <Field label="奖励上限（quota）" hint="上下限相同即固定额度">
            <Input type="number" min={0} value={s.reward_max} onChange={num('reward_max')} className="w-40" />
          </Field>
          <Field label="时区" hint="决定「今天」的边界，例如 Asia/Shanghai">
            <Input value={s.timezone} onChange={str('timezone')} className="w-56" />
          </Field>
        </div>
      </FeatureCard>
    </div>
  )
}

/**
 * PatrolHealth labels one patrol's state.
 *
 * A patrol that is switched off is labelled 停用 rather than showing an empty
 * time: without that distinction "not run yet" and "not running" look identical,
 * and the second one is the state that silently loses data.
 */
function PatrolHealth({ state, on }: { state?: PatrolState; on: boolean }) {
  if (!on) return <Badge>停用</Badge>
  if (state?.last_error) return <Badge tone="err">上次失败</Badge>
  if (state?.last_run) return <Badge tone="ok">正常</Badge>
  return <Badge tone="warn">尚未运行</Badge>
}

/**
 * summarise turns a sweep's own result into one line.
 *
 * Each patrol reports different counters, and "完成" alone would hide the case
 * the operator is looking for: a run that succeeded but found nothing to do
 * (every member already bound, no rule matched) is not the same as one that did
 * work, and only the numbers tell them apart.
 */
function summarise(spec: PatrolSpec, result: unknown): string {
  const r = (result ?? {}) as Record<string, number | string | undefined>
  const n = (k: string) => (typeof r[k] === 'number' ? (r[k] as number) : 0)

  switch (spec.key) {
    case 'members':
      // The roster sweep returns nothing; its effect is the member table.
      return '已重读全部监测群的成员列表'
    case 'bind':
      return `新绑定 ${n('bound')} 人，待注册 ${n('pending')} 人，无法匹配 ${n('ambiguous')} 人，失败 ${n('failed')} 次，已有绑定跳过 ${n('skipped')} 人`
    case 'checkins':
      return `发放奖励 ${n('awarded')} 人，未绑定跳过 ${n('unbound')} 人，发放失败 ${n('failed')} 次`
    case 'rules': {
      const rules = Array.isArray(result) ? (result as Record<string, number | string>[]) : []
      if (rules.length === 0) return '没有到期的分组转换规则'
      const moved = rules.reduce((a, r) => a + (typeof r.promoted === 'number' ? r.promoted : 0), 0)
      const failed = rules.reduce((a, r) => a + (typeof r.failed === 'number' ? r.failed : 0), 0)
      const errs = rules.filter((r) => r.error).map((r) => `${r.rule_name}：${r.error}`)
      const head = `${rules.length} 条规则，转换 ${moved} 个账号，失败 ${failed} 次`
      return errs.length ? `${head}（${errs.join('；')}）` : head
    }
    default:
      return '完成'
  }
}
