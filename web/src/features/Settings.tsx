/**
 * SettingsView is the 监测设置 tab: every automated path, its switch, and its
 * parameters in one place.
 *
 * The page is organised by what each block does — 绑定, 签到与奖励, 巡检 — rather
 * than by which code path happens to run it, because that is the question the
 * operator is actually answering ("do I want this?"). Each block owns its own
 * switch, so a feature can be turned off without hunting for a second control
 * somewhere else on the page.
 */
import { useCallback, useEffect, useState } from 'react'
import { api, type PatrolState, type Settings } from '../api'
import { Badge, Button, Card, ErrorNote, Field, Input, Select } from '../ui'
import { CommandList, FeatureCard } from '../features/common'

/**
 * DEFAULT_CHECKIN_PATTERNS mirrors the server's shipped list, so switching from
 * 任意消息 back to 按指令 restores a usable list rather than an empty one that
 * would silently check nobody in.
 */
const DEFAULT_CHECKIN_PATTERNS = '/签到\n<签到>\n>>签到<<'

/** Patrol names, in the order the page shows them. */
const PATROLS: { key: string; label: string; enabled: (s: Settings) => boolean }[] = [
  { key: 'members', label: '成员列表巡检', enabled: (s) => s.member_patrol_enabled },
  { key: 'bind', label: '绑定巡检', enabled: (s) => s.bind_patrol_enabled },
  { key: 'checkins', label: '打卡榜巡检', enabled: (s) => s.checkin_patrol_enabled },
  { key: 'rules', label: '分组转换巡检', enabled: () => true },
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
  const [patrols, setPatrols] = useState<Record<string, PatrolState>>({})
  const [busy, setBusy] = useState(false)
  const [loadErr, setLoadErr] = useState('')

  const load = useCallback(async () => {
    try {
      const r = await api.status()
      setS(r.settings)
      setPatrols(r.patrols ?? {})
      setLoadErr('')
    } catch (e) {
      setLoadErr(e instanceof Error ? e.message : String(e))
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
  const anyMessage = s.checkin_patterns.trim() === ''

  const save = async () => {
    setBusy(true)
    try {
      setS(await api.saveSettings(s))
      onDone('设置已保存')
      await onRefresh()
      await load()
    } catch (e) {
      onFail(e)
      // A rejected save (a bad regex, say) leaves the server's copy untouched:
      // reload so the form shows what is actually stored.
      await load()
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="space-y-4">
      <Card
        title="监测设置"
        description="每一类自动化都有独立的开关；关掉的功能不会产生任何请求。保存后立即生效。"
        actions={
          <Button kind="primary" disabled={busy} onClick={save}>
            {busy ? '保存中…' : '保存'}
          </Button>
        }
      >
        <PatrolTable patrols={patrols} settings={s} />
      </Card>

      {/* ---- 绑定 ---- */}
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
          hint="一行一个指令，整条消息完全匹配才生效（前后空格会被忽略）。用 /表达式/ 表示正则，例如 /^绑定\s*\d*$/；/表达式/i 忽略大小写。"
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
        title="绑定巡检"
        description="周期性重试所有未绑定的成员，用于补上「先进群、后注册」的账号。"
        enabled={s.bind_patrol_enabled}
        onToggle={bool('bind_patrol_enabled')}
      >
        <Field label="巡检间隔" hint="0 表示只保留开关、不按时执行">
          <div className="flex items-center gap-2">
            <Input
              type="number"
              min={0}
              value={s.bind_patrol_minutes}
              onChange={num('bind_patrol_minutes')}
              ariaLabel="绑定巡检间隔（分钟）"
              className="w-24"
            />
            <span className="text-xs whitespace-nowrap text-muted-foreground">分钟</span>
          </div>
        </Field>
      </FeatureCard>

      {/* ---- 签到 ---- */}
      <FeatureCard
        title="消息签到"
        description="把群里的消息当作当天签到。每天每人只记一次，重复消息不会重复发奖。"
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
            onChange={(v) => setS({ ...s, checkin_patterns: v === 'any' ? '' : DEFAULT_CHECKIN_PATTERNS })}
            ariaLabel="签到触发方式"
            className="w-56"
          >
            <option value="any">任意消息</option>
            <option value="command">按指令</option>
          </Select>
        </Field>
        {anyMessage ? (
          <p className="text-[11px] leading-4 text-muted-foreground">
            当前配置下，群里的任意一条消息都会为发送者记录当天签到。
          </p>
        ) : (
          <CommandList
            value={s.checkin_patterns}
            onChange={str('checkin_patterns')}
            hint="一行一个指令，整条消息完全匹配才生效（前后空格会被忽略）。用 /表达式/ 表示正则，例如 /^签到/；/表达式/i 忽略大小写。"
          />
        )}
      </FeatureCard>

      <FeatureCard
        title="打卡榜巡检"
        description="读取 QQ 群自带的打卡榜，补全排名与打卡时间。打卡没有事件通知，只能轮询；关闭后「用打卡榜签到」这条来源停止（消息签到不受影响）。"
        enabled={s.checkin_patrol_enabled}
        onToggle={bool('checkin_patrol_enabled')}
        badge={<Badge tone="warn">轮询</Badge>}
      >
        <Field
          label="巡检间隔"
          hint="打卡榜只显示当天，间隔越长越容易漏掉早打卡的成员。"
        >
          <div className="flex items-center gap-2">
            <Input
              type="number"
              min={0}
              value={s.checkin_sync_minutes}
              onChange={num('checkin_sync_minutes')}
              ariaLabel="打卡榜巡检间隔（分钟）"
              className="w-24"
            />
            <span className="text-xs whitespace-nowrap text-muted-foreground">分钟</span>
          </div>
        </Field>
      </FeatureCard>

      <FeatureCard
        title="签到奖励"
        description="签到成功后按区间随机发放额度；同一天同一人只发一次。"
        badge={<Badge>始终启用</Badge>}
      >
        <div className="grid gap-x-6 gap-y-5 sm:grid-cols-3">
          <Field label="奖励下限（quota）">
            <Input type="number" min={0} value={s.reward_min} onChange={num('reward_min')} className="w-40" />
          </Field>
          <Field label="奖励上限（quota）" hint="上下限相同即固定金额">
            <Input type="number" min={0} value={s.reward_max} onChange={num('reward_max')} className="w-40" />
          </Field>
          <Field label="时区" hint="决定「今天」的边界，例如 Asia/Shanghai">
            <Input value={s.timezone} onChange={str('timezone')} className="w-56" />
          </Field>
        </div>
      </FeatureCard>

      {/* ---- 成员巡检 ---- */}
      <FeatureCard
        title="群成员巡检"
        description="兜底功能：定期重读群成员列表，补齐昵称、群等级等资料。新成员进群与绑定成功都会立即记录，关闭后只是资料更新变慢。"
        enabled={s.member_patrol_enabled}
        onToggle={bool('member_patrol_enabled')}
        badge={<Badge tone="warn">轮询</Badge>}
      >
        <Field label="巡检间隔">
          <div className="flex items-center gap-2">
            <Input
              type="number"
              min={0}
              value={s.member_sync_minutes}
              onChange={num('member_sync_minutes')}
              ariaLabel="成员列表巡检间隔（分钟）"
              className="w-24"
            />
            <span className="text-xs whitespace-nowrap text-muted-foreground">分钟</span>
          </div>
        </Field>
      </FeatureCard>
    </div>
  )
}

/**
 * PatrolTable shows when each sweep last ran.
 *
 * A patrol that is switched off is labelled 停用 rather than showing an empty
 * time: without that distinction "not run yet" and "not running" look identical,
 * and the second one is the state that silently loses data.
 */
function PatrolTable({ patrols, settings }: { patrols: Record<string, PatrolState>; settings: Settings }) {
  return (
    <ul className="space-y-2">
      {PATROLS.map(({ key, label, enabled }) => {
        const on = enabled(settings)
        const p = patrols[key]
        return (
          <li key={key} className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs">
            <span className="w-32 shrink-0 text-muted-foreground">{label}</span>
            {!on ? (
              <Badge>停用</Badge>
            ) : p?.last_error ? (
              <Badge tone="err">上次失败</Badge>
            ) : p?.last_run ? (
              <Badge tone="ok">正常</Badge>
            ) : (
              <Badge tone="warn">尚未运行</Badge>
            )}
            <span className="min-w-0 break-all text-muted-foreground">
              {!on
                ? '已在下方关闭'
                : p?.last_error
                  ? p.last_error
                  : p?.last_run
                    ? `上次运行 ${new Date(p.last_run * 1000).toLocaleString()}`
                    : '进程启动后会在几十秒内首次运行'}
            </span>
          </li>
        )
      })}
    </ul>
  )
}
