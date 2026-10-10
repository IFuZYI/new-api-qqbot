import { createContext, useCallback, useContext, useEffect, useState, type ReactNode } from 'react'

export type Theme = 'light' | 'dark'

const STORAGE_KEY = 'qqbot.theme'

const ThemeContext = createContext<{ theme: Theme; setTheme: (t: Theme) => void }>({
  theme: 'dark',
  setTheme: () => {},
})

/** The stored choice, else the OS preference, else dark (the original look). */
function initialTheme(): Theme {
  try {
    const saved = localStorage.getItem(STORAGE_KEY)
    if (saved === 'light' || saved === 'dark') return saved
  } catch {
    // localStorage can be unavailable (private mode); fall through.
  }
  if (typeof window !== 'undefined' && window.matchMedia) {
    return window.matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark'
  }
  return 'dark'
}

export function ThemeProvider({ children }: { children: ReactNode }) {
  const [theme, setThemeState] = useState<Theme>(initialTheme)

  // The tokens are defined on `.dark`, so the class on <html> is what switches
  // the whole palette.
  useEffect(() => {
    const root = document.documentElement
    root.classList.toggle('dark', theme === 'dark')
    // Keeps form controls and scrollbars in step with the palette.
    root.style.colorScheme = theme
  }, [theme])

  const setTheme = useCallback((t: Theme) => {
    setThemeState(t)
    try {
      localStorage.setItem(STORAGE_KEY, t)
    } catch {
      // Not fatal: the choice just will not survive a reload.
    }
  }, [])

  return <ThemeContext.Provider value={{ theme, setTheme }}>{children}</ThemeContext.Provider>
}

export function useTheme() {
  return useContext(ThemeContext)
}

/** Light/dark switch. */
export function ThemeToggle() {
  const { theme, setTheme } = useTheme()
  const next: Theme = theme === 'dark' ? 'light' : 'dark'
  return (
    <button
      type="button"
      onClick={() => setTheme(next)}
      title={next === 'light' ? '切换到日间模式' : '切换到夜间模式'}
      aria-label={next === 'light' ? '切换到日间模式' : '切换到夜间模式'}
      className="rounded-md border border-border px-2 py-1 text-xs text-muted-foreground transition hover:bg-accent hover:text-accent-foreground"
    >
      {theme === 'dark' ? '日间模式' : '夜间模式'}
    </button>
  )
}
