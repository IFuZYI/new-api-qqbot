import React from 'react'
import ReactDOM from 'react-dom/client'
import App from './App'
import { ThemeProvider } from './theme'
import { ToastProvider } from './ui'
import './index.css'

// ToastProvider sits above App so an action's confirmation survives a tab
// change: the operator often switches tabs right after acting, and a message
// owned by the unmounted view would vanish with it.
ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <ThemeProvider>
      <ToastProvider>
        <App />
      </ToastProvider>
    </ThemeProvider>
  </React.StrictMode>,
)
