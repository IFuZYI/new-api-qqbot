/** Typed client for the qqbot API. */

export interface NewAPIConfig {
  url: string
  username: string
  password: string
}

export interface NapCatConfig {
  webui_url: string
  webui_token: string
  onebot_url: string
  onebot_token: string
  event_ws_url: string
  event_ws_token: string
}

export interface Config {
  newapi: NewAPIConfig
  napcat: NapCatConfig
}

export interface Settings {
  source_group: string
  target_group: string
  member_sync_minutes: number
  checkin_sync_minutes: number
  reward_min: number
  reward_max: number
  timezone: string
}

export interface Group {
  group_id: number
  group_name: string
  member_count: number
  enabled: boolean
  updated_at: number
}

export interface Member {
  qq: number
  nickname: string
  group_ids: number[] | null
  bound: boolean
  newapi_user_id?: number
  newapi_username?: string
  newapi_email?: string
  bind_source?: string
  checked_today: boolean
  checkin_days: number
  reward_today: number
}

export interface MembersResponse {
  items: Member[]
  total: number
  date: string
}

export interface Binding {
  qq: number
  newapi_user_id: number
  newapi_username: string
  newapi_email: string
  source: string
  created_at: number
  updated_at: number
}

export interface Bootstrap {
  configured: boolean
  settings: Settings
  groups: Group[] | null
}

async function req<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, {
    ...init,
    headers: { 'Content-Type': 'application/json', ...(init?.headers ?? {}) },
  })
  const text = await res.text()
  let body: unknown = null
  try {
    body = text ? JSON.parse(text) : null
  } catch {
    throw new Error(`${res.status}: ${text.slice(0, 200)}`)
  }
  if (!res.ok) {
    const msg = (body as { error?: string })?.error ?? `HTTP ${res.status}`
    throw new Error(msg)
  }
  return body as T
}

export interface AuthState {
  needs_setup: boolean
  authenticated: boolean
  username: string
}

export const api = {
  authState: () => req<AuthState>('/api/auth/state'),
  setup: (username: string, password: string) =>
    req<{ username: string }>('/api/auth/setup', {
      method: 'POST',
      body: JSON.stringify({ username, password }),
    }),
  login: (username: string, password: string) =>
    req<{ username: string }>('/api/auth/login', {
      method: 'POST',
      body: JSON.stringify({ username, password }),
    }),
  logout: () => req<{ ok: boolean }>('/api/auth/logout', { method: 'POST', body: '{}' }),
  changePassword: (current: string, next: string) =>
    req<{ ok: boolean }>('/api/auth/password', {
      method: 'POST',
      body: JSON.stringify({ current, next }),
    }),

  bootstrap: () => req<Bootstrap>('/api/bootstrap'),
  getConfig: () => req<Config>('/api/config'),
  saveConfig: (c: Config) => req<Config>('/api/config', { method: 'POST', body: JSON.stringify(c) }),

  groups: () => req<{ items: Group[] }>('/api/groups'),
  refreshGroups: () => req<Group[]>('/api/groups/refresh', { method: 'POST', body: '{}' }),
  saveGroups: (ids: number[]) =>
    req<Group[]>('/api/groups', { method: 'POST', body: JSON.stringify({ group_ids: ids }) }),

  members: () => req<MembersResponse>('/api/members'),

  bindings: () => req<{ items: Binding[] }>('/api/bindings'),
  createBinding: (b: { qq: number; newapi_user_id: number; newapi_username?: string; newapi_email?: string }) =>
    req<Binding>('/api/bindings', { method: 'POST', body: JSON.stringify(b) }),
  deleteBinding: (qq: number) =>
    req<{ ok: boolean }>(`/api/bindings/${qq}/delete`, { method: 'POST', body: '{}' }),

  settings: () => req<Settings>('/api/settings'),
  saveSettings: (s: Settings) =>
    req<Settings>('/api/settings', { method: 'POST', body: JSON.stringify(s) }),

  action: (name: string) => req<Record<string, unknown>>(`/api/actions/${name}`, { method: 'POST', body: '{}' }),
}
