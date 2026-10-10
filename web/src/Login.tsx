import { useState } from 'react'
import { api } from './api'
import { Button, Card, ErrorNote, Field, Input } from './ui'

/**
 * Sign-in screen. Doubles as first-run setup: when no admin exists yet, it asks
 * for a new username and password instead of an existing one.
 */
export default function Login({ needsSetup, onDone }: { needsSetup: boolean; onDone: () => void }) {
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [confirm, setConfirm] = useState('')
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)

  const submit = async () => {
    setErr('')
    if (!username.trim()) {
      setErr('请输入用户名')
      return
    }
    if (needsSetup) {
      if (password.length < 8) {
        setErr('密码至少 8 位')
        return
      }
      if (password !== confirm) {
        setErr('两次输入的密码不一致')
        return
      }
    } else if (!password) {
      setErr('请输入密码')
      return
    }

    setBusy(true)
    try {
      if (needsSetup) await api.setup(username.trim(), password)
      else await api.login(username.trim(), password)
      onDone()
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="flex min-h-full items-center justify-center p-4">
      <div className="w-full max-w-sm">
        <div className="mb-6 flex flex-col items-center text-center">
          <span
            aria-hidden="true"
            className="mb-3 flex size-10 items-center justify-center rounded-surface bg-primary text-primary-foreground"
          >
            <svg viewBox="0 0 16 16" className="size-5">
              <path
                d="M2.5 4.5h11M2.5 8h7.5M2.5 11.5h9.5"
                fill="none"
                stroke="currentColor"
                strokeWidth="1.75"
                strokeLinecap="round"
              />
            </svg>
          </span>
          <h1 className="text-base font-semibold tracking-tight">QQBot 群监测</h1>
          <p className="mt-1 text-xs text-muted-foreground">
            {needsSetup ? '首次使用，请设置管理账号' : '请登录管理后台'}
          </p>
        </div>
        <Card>
          {err && <ErrorNote onClose={() => setErr('')}>{err}</ErrorNote>}
          <form
            className="space-y-4"
            onSubmit={(e) => {
              e.preventDefault()
              void submit()
            }}
          >
            <Field label="用户名">
              <Input
                value={username}
                onChange={setUsername}
                placeholder="admin"
                autoComplete="username"
                autoFocus
              />
            </Field>
            <Field label="密码" hint={needsSetup ? '至少 8 位' : undefined}>
              <Input
                type="password"
                value={password}
                onChange={setPassword}
                autoComplete={needsSetup ? 'new-password' : 'current-password'}
              />
            </Field>
            {needsSetup && (
              <Field label="确认密码">
                <Input type="password" value={confirm} onChange={setConfirm} autoComplete="new-password" />
              </Field>
            )}
            <div className="pt-1">
              <Button kind="primary" type="submit" disabled={busy} full>
                {busy ? '处理中…' : needsSetup ? '创建并登录' : '登录'}
              </Button>
            </div>
          </form>
        </Card>
        <p className="mt-4 text-center text-[11px] leading-4 text-muted-foreground">
          这是本服务自己的管理账号，与 new-api 无关。
        </p>
      </div>
    </div>
  )
}
