import { useId } from 'react'

export type IconProps = { className?: string; size?: number; title?: string }

type SvgWrapperProps = IconProps & { children: (id: string) => React.ReactNode }

function SvgWrapper({ className, size = 20, title, children }: SvgWrapperProps) {
  const id = useId()
  const ariaProps = title
    ? { role: 'img' as const }
    : { 'aria-hidden': true as const, focusable: 'false' as const }

  return (
    <svg
      xmlns="http://www.w3.org/2000/svg"
      viewBox="0 0 24 24"
      width={size}
      height={size}
      className={className}
      {...ariaProps}
    >
      {title && <title>{title}</title>}
      {children(id)}
    </svg>
  )
}

// ─── VIEWS ──────────────────────────────────────────────────────────────────────

export function IconDashboard(props: IconProps) {
  return (
    <SvgWrapper {...props}>
      {(id) => (
        <>
          <defs>
            <linearGradient id={`${id}-a`} x1="0%" y1="0%" x2="100%" y2="100%">
              <stop offset="0%" stopColor="#38bdf8" />
              <stop offset="100%" stopColor="#818cf8" />
            </linearGradient>
          </defs>
          <rect x="3" y="3" width="8" height="8" rx="1.5" fill={`url(#${id}-a)`} />
          <rect x="13" y="3" width="8" height="5" rx="1.5" fill={`url(#${id}-a)`} opacity="0.7" />
          <rect x="13" y="10" width="8" height="11" rx="1.5" fill={`url(#${id}-a)`} opacity="0.85" />
          <rect x="3" y="13" width="8" height="8" rx="1.5" fill={`url(#${id}-a)`} opacity="0.55" />
        </>
      )}
    </SvgWrapper>
  )
}

export function IconChannels(props: IconProps) {
  return (
    <SvgWrapper {...props}>
      {(id) => (
        <>
          <defs>
            <linearGradient id={`${id}-a`} x1="0%" y1="0%" x2="100%" y2="100%">
              <stop offset="0%" stopColor="#2dd4bf" />
              <stop offset="100%" stopColor="#22d3ee" />
            </linearGradient>
          </defs>
          <path d="M4 6h16M4 12h16M4 18h16" stroke={`url(#${id}-a)`} strokeWidth="2" strokeLinecap="round" fill="none" />
          <circle cx="8" cy="6" r="2" fill="#2dd4bf" />
          <circle cx="16" cy="12" r="2" fill="#22d3ee" />
          <circle cx="10" cy="18" r="2" fill="#5eead4" />
        </>
      )}
    </SvgWrapper>
  )
}

export function IconMessages(props: IconProps) {
  return (
    <SvgWrapper {...props}>
      {(id) => (
        <>
          <defs>
            <linearGradient id={`${id}-a`} x1="0%" y1="0%" x2="100%" y2="100%">
              <stop offset="0%" stopColor="#a78bfa" />
              <stop offset="100%" stopColor="#c084fc" />
            </linearGradient>
          </defs>
          <path d="M21 15a2 2 0 0 1-2 2H7l-4 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2z" fill={`url(#${id}-a)`} />
          <path d="M8 9h8M8 13h5" stroke="#fff" strokeWidth="1.5" strokeLinecap="round" fill="none" opacity="0.8" />
        </>
      )}
    </SvgWrapper>
  )
}

export function IconQueue(props: IconProps) {
  return (
    <SvgWrapper {...props}>
      {(id) => (
        <>
          <defs>
            <linearGradient id={`${id}-a`} x1="0%" y1="0%" x2="100%" y2="100%">
              <stop offset="0%" stopColor="#fb923c" />
              <stop offset="100%" stopColor="#fbbf24" />
            </linearGradient>
          </defs>
          <rect x="3" y="4" width="18" height="4" rx="1" fill={`url(#${id}-a)`} />
          <rect x="3" y="10" width="18" height="4" rx="1" fill={`url(#${id}-a)`} opacity="0.7" />
          <rect x="3" y="16" width="18" height="4" rx="1" fill={`url(#${id}-a)`} opacity="0.45" />
        </>
      )}
    </SvgWrapper>
  )
}

export function IconAlerts(props: IconProps) {
  return (
    <SvgWrapper {...props}>
      {(id) => (
        <>
          <defs>
            <linearGradient id={`${id}-a`} x1="0%" y1="0%" x2="100%" y2="100%">
              <stop offset="0%" stopColor="#f43f5e" />
              <stop offset="100%" stopColor="#fb923c" />
            </linearGradient>
          </defs>
          <path d="M12 2L2 20h20L12 2z" fill={`url(#${id}-a)`} />
          <path d="M12 9v4M12 16h.01" stroke="#fff" strokeWidth="2" strokeLinecap="round" fill="none" />
        </>
      )}
    </SvgWrapper>
  )
}

export function IconMetrics(props: IconProps) {
  return (
    <SvgWrapper {...props}>
      {(id) => (
        <>
          <defs>
            <linearGradient id={`${id}-a`} x1="0%" y1="100%" x2="100%" y2="0%">
              <stop offset="0%" stopColor="#6366f1" />
              <stop offset="100%" stopColor="#06b6d4" />
            </linearGradient>
          </defs>
          <polyline points="3,18 8,12 12,14 16,8 21,4" stroke={`url(#${id}-a)`} strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" fill="none" />
          <circle cx="8" cy="12" r="1.5" fill="#6366f1" />
          <circle cx="12" cy="14" r="1.5" fill="#8b5cf6" />
          <circle cx="16" cy="8" r="1.5" fill="#06b6d4" />
          <circle cx="21" cy="4" r="1.5" fill="#22d3ee" />
        </>
      )}
    </SvgWrapper>
  )
}

export function IconFhirLab(props: IconProps) {
  return (
    <SvgWrapper {...props}>
      {(id) => (
        <>
          <defs>
            <linearGradient id={`${id}-a`} x1="0%" y1="0%" x2="100%" y2="100%">
              <stop offset="0%" stopColor="#f472b6" />
              <stop offset="50%" stopColor="#e879f9" />
              <stop offset="100%" stopColor="#818cf8" />
            </linearGradient>
          </defs>
          <path d="M9 3v7l-4 8a2 2 0 0 0 1.8 3h10.4a2 2 0 0 0 1.8-3l-4-8V3" fill="none" stroke={`url(#${id}-a)`} strokeWidth="2" strokeLinecap="round" />
          <path d="M9 3h6" stroke={`url(#${id}-a)`} strokeWidth="2" strokeLinecap="round" />
          <circle cx="10" cy="16" r="1" fill="#f472b6" />
          <circle cx="13" cy="14" r="1.2" fill="#e879f9" />
          <circle cx="14" cy="17" r="0.8" fill="#818cf8" />
        </>
      )}
    </SvgWrapper>
  )
}

export function IconDocuments(props: IconProps) {
  return (
    <SvgWrapper {...props}>
      {(id) => (
        <>
          <defs>
            <linearGradient id={`${id}-a`} x1="0%" y1="0%" x2="100%" y2="100%">
              <stop offset="0%" stopColor="#60a5fa" />
              <stop offset="100%" stopColor="#34d399" />
            </linearGradient>
          </defs>
          <path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8l-6-6z" fill={`url(#${id}-a)`} />
          <polyline points="14,2 14,8 20,8" fill="none" stroke="#fff" strokeWidth="1" opacity="0.6" />
          <path d="M8 13h8M8 17h5" stroke="#fff" strokeWidth="1.5" strokeLinecap="round" fill="none" opacity="0.7" />
        </>
      )}
    </SvgWrapper>
  )
}

export function IconScripts(props: IconProps) {
  return (
    <SvgWrapper {...props}>
      {(id) => (
        <>
          <defs>
            <linearGradient id={`${id}-a`} x1="0%" y1="0%" x2="100%" y2="100%">
              <stop offset="0%" stopColor="#fbbf24" />
              <stop offset="100%" stopColor="#f97316" />
            </linearGradient>
          </defs>
          <rect x="3" y="3" width="18" height="18" rx="3" fill="#1e293b" stroke={`url(#${id}-a)`} strokeWidth="1.5" />
          <path d="M8 8l3 3-3 3" stroke={`url(#${id}-a)`} strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" fill="none" />
          <path d="M13 16h4" stroke={`url(#${id}-a)`} strokeWidth="2" strokeLinecap="round" fill="none" />
        </>
      )}
    </SvgWrapper>
  )
}

export function IconMigrate(props: IconProps) {
  return (
    <SvgWrapper {...props}>
      {(id) => (
        <>
          <defs>
            <linearGradient id={`${id}-a`} x1="0%" y1="0%" x2="100%" y2="0%">
              <stop offset="0%" stopColor="#f472b6" />
              <stop offset="100%" stopColor="#c084fc" />
            </linearGradient>
          </defs>
          <path d="M5 9h14l-4-4" stroke={`url(#${id}-a)`} strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" fill="none" />
          <path d="M19 15H5l4 4" stroke={`url(#${id}-a)`} strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" fill="none" />
        </>
      )}
    </SvgWrapper>
  )
}

export function IconContracts(props: IconProps) {
  return (
    <SvgWrapper {...props}>
      {(id) => (
        <>
          <defs>
            <linearGradient id={`${id}-a`} x1="0%" y1="0%" x2="100%" y2="100%">
              <stop offset="0%" stopColor="#a78bfa" />
              <stop offset="100%" stopColor="#6366f1" />
            </linearGradient>
          </defs>
          <path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8l-6-6z" fill={`url(#${id}-a)`} />
          <path d="M9 15l2 2 4-4" stroke="#fff" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" fill="none" />
        </>
      )}
    </SvgWrapper>
  )
}

export function IconTables(props: IconProps) {
  return (
    <SvgWrapper {...props}>
      {(id) => (
        <>
          <defs>
            <linearGradient id={`${id}-a`} x1="0%" y1="0%" x2="100%" y2="100%">
              <stop offset="0%" stopColor="#14b8a6" />
              <stop offset="100%" stopColor="#0ea5e9" />
            </linearGradient>
          </defs>
          <rect x="3" y="3" width="18" height="18" rx="2" fill="none" stroke={`url(#${id}-a)`} strokeWidth="1.5" />
          <path d="M3 9h18M3 15h18M9 3v18M15 3v18" stroke={`url(#${id}-a)`} strokeWidth="1.5" fill="none" />
        </>
      )}
    </SvgWrapper>
  )
}

export function IconMapper(props: IconProps) {
  return (
    <SvgWrapper {...props}>
      {(id) => (
        <>
          <defs>
            <linearGradient id={`${id}-a`} x1="0%" y1="0%" x2="100%" y2="100%">
              <stop offset="0%" stopColor="#e879f9" />
              <stop offset="50%" stopColor="#a78bfa" />
              <stop offset="100%" stopColor="#60a5fa" />
            </linearGradient>
          </defs>
          <circle cx="5" cy="6" r="2" fill="#e879f9" />
          <circle cx="5" cy="12" r="2" fill="#a78bfa" />
          <circle cx="5" cy="18" r="2" fill="#60a5fa" />
          <circle cx="19" cy="8" r="2" fill="#c084fc" />
          <circle cx="19" cy="16" r="2" fill="#818cf8" />
          <path d="M7 6l10 2M7 12h10M7 18l10-2" stroke={`url(#${id}-a)`} strokeWidth="1.5" fill="none" />
        </>
      )}
    </SvgWrapper>
  )
}

export function IconFleet(props: IconProps) {
  return (
    <SvgWrapper {...props}>
      {(id) => (
        <>
          <defs>
            <linearGradient id={`${id}-a`} x1="0%" y1="0%" x2="100%" y2="100%">
              <stop offset="0%" stopColor="#22d3ee" />
              <stop offset="100%" stopColor="#6366f1" />
            </linearGradient>
          </defs>
          <rect x="2" y="4" width="8" height="6" rx="1" fill={`url(#${id}-a)`} />
          <rect x="14" y="4" width="8" height="6" rx="1" fill={`url(#${id}-a)`} opacity="0.8" />
          <rect x="2" y="14" width="8" height="6" rx="1" fill={`url(#${id}-a)`} opacity="0.6" />
          <rect x="14" y="14" width="8" height="6" rx="1" fill={`url(#${id}-a)`} opacity="0.4" />
          <circle cx="5" cy="7" r="1" fill="#fff" opacity="0.8" />
          <circle cx="17" cy="7" r="1" fill="#fff" opacity="0.8" />
          <circle cx="5" cy="17" r="1" fill="#fff" opacity="0.8" />
          <circle cx="17" cy="17" r="1" fill="#fff" opacity="0.8" />
        </>
      )}
    </SvgWrapper>
  )
}

export function IconFlowMap(props: IconProps) {
  return (
    <SvgWrapper {...props}>
      {(id) => (
        <>
          <defs>
            <linearGradient id={`${id}-a`} x1="0%" y1="0%" x2="100%" y2="100%">
              <stop offset="0%" stopColor="#34d399" />
              <stop offset="100%" stopColor="#0ea5e9" />
            </linearGradient>
          </defs>
          <circle cx="5" cy="12" r="3" fill="#34d399" />
          <circle cx="19" cy="6" r="3" fill="#0ea5e9" />
          <circle cx="19" cy="18" r="3" fill="#22d3ee" />
          <path d="M8 11l8-4M8 13l8 4" stroke={`url(#${id}-a)`} strokeWidth="2" fill="none" />
        </>
      )}
    </SvgWrapper>
  )
}

export function IconPlayground(props: IconProps) {
  return (
    <SvgWrapper {...props}>
      {(id) => (
        <>
          <defs>
            <linearGradient id={`${id}-a`} x1="0%" y1="0%" x2="100%" y2="100%">
              <stop offset="0%" stopColor="#fbbf24" />
              <stop offset="50%" stopColor="#f97316" />
              <stop offset="100%" stopColor="#ef4444" />
            </linearGradient>
          </defs>
          <polygon points="5,3 19,12 5,21" fill={`url(#${id}-a)`} />
        </>
      )}
    </SvgWrapper>
  )
}

export function IconShadow(props: IconProps) {
  return (
    <SvgWrapper {...props}>
      {(id) => (
        <>
          <defs>
            <linearGradient id={`${id}-a`} x1="0%" y1="0%" x2="100%" y2="100%">
              <stop offset="0%" stopColor="#64748b" />
              <stop offset="100%" stopColor="#a78bfa" />
            </linearGradient>
          </defs>
          <circle cx="12" cy="12" r="9" fill={`url(#${id}-a)`} />
          <path d="M12 3a9 9 0 0 1 0 18" fill="#1e293b" opacity="0.6" />
        </>
      )}
    </SvgWrapper>
  )
}

export function IconCertificates(props: IconProps) {
  return (
    <SvgWrapper {...props}>
      {(id) => (
        <>
          <defs>
            <linearGradient id={`${id}-a`} x1="0%" y1="0%" x2="100%" y2="100%">
              <stop offset="0%" stopColor="#fbbf24" />
              <stop offset="100%" stopColor="#f59e0b" />
            </linearGradient>
          </defs>
          <circle cx="12" cy="10" r="6" fill="none" stroke={`url(#${id}-a)`} strokeWidth="2" />
          <path d="M12 16v5M9 19l3 2 3-2" stroke={`url(#${id}-a)`} strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" fill="none" />
          <path d="M10 8l1.5 1.5 3-3" stroke={`url(#${id}-a)`} strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" fill="none" />
        </>
      )}
    </SvgWrapper>
  )
}

export function IconUsers(props: IconProps) {
  return (
    <SvgWrapper {...props}>
      {(id) => (
        <>
          <defs>
            <linearGradient id={`${id}-a`} x1="0%" y1="0%" x2="100%" y2="100%">
              <stop offset="0%" stopColor="#38bdf8" />
              <stop offset="100%" stopColor="#a78bfa" />
            </linearGradient>
          </defs>
          <circle cx="9" cy="7" r="3" fill={`url(#${id}-a)`} />
          <path d="M3 21v-2a4 4 0 0 1 4-4h4a4 4 0 0 1 4 4v2" fill={`url(#${id}-a)`} opacity="0.7" />
          <circle cx="17" cy="7" r="2.5" fill={`url(#${id}-a)`} opacity="0.6" />
          <path d="M17 12.5a3 3 0 0 1 3 3V21" stroke={`url(#${id}-a)`} strokeWidth="1.5" fill="none" opacity="0.5" />
        </>
      )}
    </SvgWrapper>
  )
}

export function IconActivity(props: IconProps) {
  return (
    <SvgWrapper {...props}>
      {(id) => (
        <>
          <defs>
            <linearGradient id={`${id}-a`} x1="0%" y1="0%" x2="100%" y2="0%">
              <stop offset="0%" stopColor="#10b981" />
              <stop offset="50%" stopColor="#06b6d4" />
              <stop offset="100%" stopColor="#8b5cf6" />
            </linearGradient>
          </defs>
          <polyline points="2,12 6,12 9,4 12,20 15,8 18,12 22,12" stroke={`url(#${id}-a)`} strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" fill="none" />
        </>
      )}
    </SvgWrapper>
  )
}

export function IconSettings(props: IconProps) {
  return (
    <SvgWrapper {...props}>
      {(id) => (
        <>
          <defs>
            <linearGradient id={`${id}-a`} x1="0%" y1="0%" x2="100%" y2="100%">
              <stop offset="0%" stopColor="#94a3b8" />
              <stop offset="100%" stopColor="#64748b" />
            </linearGradient>
          </defs>
          <path d="M12 15a3 3 0 1 0 0-6 3 3 0 0 0 0 6z" fill={`url(#${id}-a)`} />
          <path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 1 1-4 0v-.09a1.65 1.65 0 0 0-1.08-1.51 1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06a1.65 1.65 0 0 0 .33-1.82 1.65 1.65 0 0 0-1.51-1H3a2 2 0 1 1 0-4h.09a1.65 1.65 0 0 0 1.51-1.08 1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06a1.65 1.65 0 0 0 1.82.33H9a1.65 1.65 0 0 0 1-1.51V3a2 2 0 1 1 4 0v.09a1.65 1.65 0 0 0 1.08 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06a1.65 1.65 0 0 0-.33 1.82V9c.26.6.84 1 1.51 1.08H21a2 2 0 1 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1z" fill="none" stroke={`url(#${id}-a)`} strokeWidth="1.5" />
        </>
      )}
    </SvgWrapper>
  )
}

// ─── STATUS ─────────────────────────────────────────────────────────────────────

export function IconRunning(props: IconProps) {
  return (
    <SvgWrapper {...props}>
      {(id) => (
        <>
          <defs>
            <linearGradient id={`${id}-a`} x1="0%" y1="0%" x2="100%" y2="100%">
              <stop offset="0%" stopColor="#34d399" />
              <stop offset="100%" stopColor="#10b981" />
            </linearGradient>
          </defs>
          <circle cx="12" cy="12" r="9" fill={`url(#${id}-a)`} />
          <polygon points="10,8 16,12 10,16" fill="#fff" />
        </>
      )}
    </SvgWrapper>
  )
}

export function IconStopped(props: IconProps) {
  return (
    <SvgWrapper {...props}>
      {(id) => (
        <>
          <defs>
            <linearGradient id={`${id}-a`} x1="0%" y1="0%" x2="100%" y2="100%">
              <stop offset="0%" stopColor="#f43f5e" />
              <stop offset="100%" stopColor="#e11d48" />
            </linearGradient>
          </defs>
          <circle cx="12" cy="12" r="9" fill={`url(#${id}-a)`} />
          <rect x="9" y="9" width="6" height="6" rx="0.5" fill="#fff" />
        </>
      )}
    </SvgWrapper>
  )
}

export function IconPaused(props: IconProps) {
  return (
    <SvgWrapper {...props}>
      {(id) => (
        <>
          <defs>
            <linearGradient id={`${id}-a`} x1="0%" y1="0%" x2="100%" y2="100%">
              <stop offset="0%" stopColor="#fbbf24" />
              <stop offset="100%" stopColor="#f59e0b" />
            </linearGradient>
          </defs>
          <circle cx="12" cy="12" r="9" fill={`url(#${id}-a)`} />
          <rect x="9" y="8" width="2.5" height="8" rx="0.5" fill="#fff" />
          <rect x="13" y="8" width="2.5" height="8" rx="0.5" fill="#fff" />
        </>
      )}
    </SvgWrapper>
  )
}

export function IconError(props: IconProps) {
  return (
    <SvgWrapper {...props}>
      {(id) => (
        <>
          <defs>
            <linearGradient id={`${id}-a`} x1="0%" y1="0%" x2="100%" y2="100%">
              <stop offset="0%" stopColor="#ef4444" />
              <stop offset="100%" stopColor="#dc2626" />
            </linearGradient>
          </defs>
          <circle cx="12" cy="12" r="9" fill={`url(#${id}-a)`} />
          <path d="M15 9l-6 6M9 9l6 6" stroke="#fff" strokeWidth="2" strokeLinecap="round" fill="none" />
        </>
      )}
    </SvgWrapper>
  )
}

export function IconWarning(props: IconProps) {
  return (
    <SvgWrapper {...props}>
      {(id) => (
        <>
          <defs>
            <linearGradient id={`${id}-a`} x1="0%" y1="0%" x2="100%" y2="100%">
              <stop offset="0%" stopColor="#fbbf24" />
              <stop offset="100%" stopColor="#f97316" />
            </linearGradient>
          </defs>
          <path d="M12 2L2 20h20L12 2z" fill={`url(#${id}-a)`} />
          <path d="M12 9v4" stroke="#fff" strokeWidth="2" strokeLinecap="round" fill="none" />
          <circle cx="12" cy="16" r="1" fill="#fff" />
        </>
      )}
    </SvgWrapper>
  )
}

export function IconSuccess(props: IconProps) {
  return (
    <SvgWrapper {...props}>
      {(id) => (
        <>
          <defs>
            <linearGradient id={`${id}-a`} x1="0%" y1="0%" x2="100%" y2="100%">
              <stop offset="0%" stopColor="#10b981" />
              <stop offset="100%" stopColor="#059669" />
            </linearGradient>
          </defs>
          <circle cx="12" cy="12" r="9" fill={`url(#${id}-a)`} />
          <path d="M8 12l3 3 5-5" stroke="#fff" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" fill="none" />
        </>
      )}
    </SvgWrapper>
  )
}

export function IconInfo(props: IconProps) {
  return (
    <SvgWrapper {...props}>
      {(id) => (
        <>
          <defs>
            <linearGradient id={`${id}-a`} x1="0%" y1="0%" x2="100%" y2="100%">
              <stop offset="0%" stopColor="#38bdf8" />
              <stop offset="100%" stopColor="#0ea5e9" />
            </linearGradient>
          </defs>
          <circle cx="12" cy="12" r="9" fill={`url(#${id}-a)`} />
          <path d="M12 16v-4M12 8h.01" stroke="#fff" strokeWidth="2" strokeLinecap="round" fill="none" />
        </>
      )}
    </SvgWrapper>
  )
}

export function IconPending(props: IconProps) {
  return (
    <SvgWrapper {...props}>
      {(id) => (
        <>
          <defs>
            <linearGradient id={`${id}-a`} x1="0%" y1="0%" x2="100%" y2="100%">
              <stop offset="0%" stopColor="#a78bfa" />
              <stop offset="100%" stopColor="#f59e0b" />
            </linearGradient>
          </defs>
          <circle cx="12" cy="12" r="9" fill="none" stroke={`url(#${id}-a)`} strokeWidth="2" />
          <path d="M12 7v5l3 3" stroke={`url(#${id}-a)`} strokeWidth="2" strokeLinecap="round" fill="none" />
        </>
      )}
    </SvgWrapper>
  )
}

// ─── ACTIONS ────────────────────────────────────────────────────────────────────

export function IconStart(props: IconProps) {
  return (
    <SvgWrapper {...props}>
      {(id) => (
        <>
          <defs>
            <linearGradient id={`${id}-a`} x1="0%" y1="0%" x2="100%" y2="100%">
              <stop offset="0%" stopColor="#34d399" />
              <stop offset="100%" stopColor="#10b981" />
            </linearGradient>
          </defs>
          <polygon points="6,3 20,12 6,21" fill={`url(#${id}-a)`} />
        </>
      )}
    </SvgWrapper>
  )
}

export function IconStop(props: IconProps) {
  return (
    <SvgWrapper {...props}>
      {(id) => (
        <>
          <defs>
            <linearGradient id={`${id}-a`} x1="0%" y1="0%" x2="100%" y2="100%">
              <stop offset="0%" stopColor="#f43f5e" />
              <stop offset="100%" stopColor="#e11d48" />
            </linearGradient>
          </defs>
          <rect x="5" y="5" width="14" height="14" rx="2" fill={`url(#${id}-a)`} />
        </>
      )}
    </SvgWrapper>
  )
}

export function IconEdit(props: IconProps) {
  return (
    <SvgWrapper {...props}>
      {(id) => (
        <>
          <defs>
            <linearGradient id={`${id}-a`} x1="0%" y1="0%" x2="100%" y2="100%">
              <stop offset="0%" stopColor="#60a5fa" />
              <stop offset="100%" stopColor="#818cf8" />
            </linearGradient>
          </defs>
          <path d="M17 3a2.83 2.83 0 1 1 4 4L7.5 20.5 2 22l1.5-5.5L17 3z" fill={`url(#${id}-a)`} />
        </>
      )}
    </SvgWrapper>
  )
}

export function IconDelete(props: IconProps) {
  return (
    <SvgWrapper {...props}>
      {(id) => (
        <>
          <defs>
            <linearGradient id={`${id}-a`} x1="0%" y1="0%" x2="100%" y2="100%">
              <stop offset="0%" stopColor="#f43f5e" />
              <stop offset="100%" stopColor="#ef4444" />
            </linearGradient>
          </defs>
          <path d="M3 6h18M8 6V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2M19 6l-1 14a2 2 0 0 1-2 2H8a2 2 0 0 1-2-2L5 6" fill="none" stroke={`url(#${id}-a)`} strokeWidth="2" strokeLinecap="round" />
          <path d="M10 11v6M14 11v6" stroke={`url(#${id}-a)`} strokeWidth="2" strokeLinecap="round" fill="none" />
        </>
      )}
    </SvgWrapper>
  )
}

export function IconCopy(props: IconProps) {
  return (
    <SvgWrapper {...props}>
      {(id) => (
        <>
          <defs>
            <linearGradient id={`${id}-a`} x1="0%" y1="0%" x2="100%" y2="100%">
              <stop offset="0%" stopColor="#38bdf8" />
              <stop offset="100%" stopColor="#22d3ee" />
            </linearGradient>
          </defs>
          <rect x="9" y="9" width="11" height="11" rx="2" fill="none" stroke={`url(#${id}-a)`} strokeWidth="2" />
          <path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1" fill="none" stroke={`url(#${id}-a)`} strokeWidth="2" />
        </>
      )}
    </SvgWrapper>
  )
}

export function IconDownload(props: IconProps) {
  return (
    <SvgWrapper {...props}>
      {(id) => (
        <>
          <defs>
            <linearGradient id={`${id}-a`} x1="0%" y1="0%" x2="0%" y2="100%">
              <stop offset="0%" stopColor="#34d399" />
              <stop offset="100%" stopColor="#0ea5e9" />
            </linearGradient>
          </defs>
          <path d="M12 3v12M12 15l-4-4M12 15l4-4" stroke={`url(#${id}-a)`} strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" fill="none" />
          <path d="M4 19h16" stroke={`url(#${id}-a)`} strokeWidth="2" strokeLinecap="round" fill="none" />
        </>
      )}
    </SvgWrapper>
  )
}

export function IconUpload(props: IconProps) {
  return (
    <SvgWrapper {...props}>
      {(id) => (
        <>
          <defs>
            <linearGradient id={`${id}-a`} x1="0%" y1="100%" x2="0%" y2="0%">
              <stop offset="0%" stopColor="#34d399" />
              <stop offset="100%" stopColor="#0ea5e9" />
            </linearGradient>
          </defs>
          <path d="M12 17V5M12 5l-4 4M12 5l4 4" stroke={`url(#${id}-a)`} strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" fill="none" />
          <path d="M4 19h16" stroke={`url(#${id}-a)`} strokeWidth="2" strokeLinecap="round" fill="none" />
        </>
      )}
    </SvgWrapper>
  )
}

export function IconSearch(props: IconProps) {
  return (
    <SvgWrapper {...props}>
      {(id) => (
        <>
          <defs>
            <linearGradient id={`${id}-a`} x1="0%" y1="0%" x2="100%" y2="100%">
              <stop offset="0%" stopColor="#60a5fa" />
              <stop offset="100%" stopColor="#a78bfa" />
            </linearGradient>
          </defs>
          <circle cx="11" cy="11" r="7" fill="none" stroke={`url(#${id}-a)`} strokeWidth="2" />
          <path d="M16 16l5 5" stroke={`url(#${id}-a)`} strokeWidth="2" strokeLinecap="round" fill="none" />
        </>
      )}
    </SvgWrapper>
  )
}

export function IconRefresh(props: IconProps) {
  return (
    <SvgWrapper {...props}>
      {(id) => (
        <>
          <defs>
            <linearGradient id={`${id}-a`} x1="0%" y1="0%" x2="100%" y2="100%">
              <stop offset="0%" stopColor="#22d3ee" />
              <stop offset="100%" stopColor="#6366f1" />
            </linearGradient>
          </defs>
          <path d="M1 4v6h6M23 20v-6h-6" stroke={`url(#${id}-a)`} strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" fill="none" />
          <path d="M20.49 9A9 9 0 0 0 5.64 5.64L1 10M23 14l-4.64 4.36A9 9 0 0 1 3.51 15" stroke={`url(#${id}-a)`} strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" fill="none" />
        </>
      )}
    </SvgWrapper>
  )
}

export function IconAdd(props: IconProps) {
  return (
    <SvgWrapper {...props}>
      {(id) => (
        <>
          <defs>
            <linearGradient id={`${id}-a`} x1="0%" y1="0%" x2="100%" y2="100%">
              <stop offset="0%" stopColor="#34d399" />
              <stop offset="100%" stopColor="#10b981" />
            </linearGradient>
          </defs>
          <circle cx="12" cy="12" r="9" fill="none" stroke={`url(#${id}-a)`} strokeWidth="2" />
          <path d="M12 8v8M8 12h8" stroke={`url(#${id}-a)`} strokeWidth="2" strokeLinecap="round" fill="none" />
        </>
      )}
    </SvgWrapper>
  )
}

export function IconClose(props: IconProps) {
  return (
    <SvgWrapper {...props}>
      {(id) => (
        <>
          <defs>
            <linearGradient id={`${id}-a`} x1="0%" y1="0%" x2="100%" y2="100%">
              <stop offset="0%" stopColor="#f43f5e" />
              <stop offset="100%" stopColor="#ef4444" />
            </linearGradient>
          </defs>
          <path d="M18 6L6 18M6 6l12 12" stroke={`url(#${id}-a)`} strokeWidth="2" strokeLinecap="round" fill="none" />
        </>
      )}
    </SvgWrapper>
  )
}

export function IconCheck(props: IconProps) {
  return (
    <SvgWrapper {...props}>
      {(id) => (
        <>
          <defs>
            <linearGradient id={`${id}-a`} x1="0%" y1="0%" x2="100%" y2="100%">
              <stop offset="0%" stopColor="#34d399" />
              <stop offset="100%" stopColor="#10b981" />
            </linearGradient>
          </defs>
          <path d="M4 12l5 5L20 6" stroke={`url(#${id}-a)`} strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round" fill="none" />
        </>
      )}
    </SvgWrapper>
  )
}

export function IconChevronDown(props: IconProps) {
  return (
    <SvgWrapper {...props}>
      {(id) => (
        <>
          <defs>
            <linearGradient id={`${id}-a`} x1="0%" y1="0%" x2="100%" y2="100%">
              <stop offset="0%" stopColor="#94a3b8" />
              <stop offset="100%" stopColor="#cbd5e1" />
            </linearGradient>
          </defs>
          <path d="M6 9l6 6 6-6" stroke={`url(#${id}-a)`} strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" fill="none" />
        </>
      )}
    </SvgWrapper>
  )
}

export function IconChevronRight(props: IconProps) {
  return (
    <SvgWrapper {...props}>
      {(id) => (
        <>
          <defs>
            <linearGradient id={`${id}-a`} x1="0%" y1="0%" x2="100%" y2="100%">
              <stop offset="0%" stopColor="#94a3b8" />
              <stop offset="100%" stopColor="#cbd5e1" />
            </linearGradient>
          </defs>
          <path d="M9 18l6-6-6-6" stroke={`url(#${id}-a)`} strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" fill="none" />
        </>
      )}
    </SvgWrapper>
  )
}

export function IconExternal(props: IconProps) {
  return (
    <SvgWrapper {...props}>
      {(id) => (
        <>
          <defs>
            <linearGradient id={`${id}-a`} x1="0%" y1="0%" x2="100%" y2="100%">
              <stop offset="0%" stopColor="#60a5fa" />
              <stop offset="100%" stopColor="#38bdf8" />
            </linearGradient>
          </defs>
          <path d="M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6" fill="none" stroke={`url(#${id}-a)`} strokeWidth="2" />
          <polyline points="15,3 21,3 21,9" stroke={`url(#${id}-a)`} strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" fill="none" />
          <path d="M10 14L21 3" stroke={`url(#${id}-a)`} strokeWidth="2" strokeLinecap="round" fill="none" />
        </>
      )}
    </SvgWrapper>
  )
}

export function IconFilter(props: IconProps) {
  return (
    <SvgWrapper {...props}>
      {(id) => (
        <>
          <defs>
            <linearGradient id={`${id}-a`} x1="0%" y1="0%" x2="100%" y2="100%">
              <stop offset="0%" stopColor="#c084fc" />
              <stop offset="100%" stopColor="#818cf8" />
            </linearGradient>
          </defs>
          <polygon points="2,3 22,3 14,13 14,21 10,19 10,13" fill={`url(#${id}-a)`} />
        </>
      )}
    </SvgWrapper>
  )
}

export function IconSave(props: IconProps) {
  return (
    <SvgWrapper {...props}>
      {(id) => (
        <>
          <defs>
            <linearGradient id={`${id}-a`} x1="0%" y1="0%" x2="100%" y2="100%">
              <stop offset="0%" stopColor="#60a5fa" />
              <stop offset="100%" stopColor="#3b82f6" />
            </linearGradient>
          </defs>
          <path d="M19 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h11l5 5v11a2 2 0 0 1-2 2z" fill={`url(#${id}-a)`} />
          <polyline points="17,21 17,13 7,13 7,21" stroke="#fff" strokeWidth="1" fill="none" opacity="0.7" />
          <polyline points="7,3 7,8 15,8" stroke="#fff" strokeWidth="1" fill="none" opacity="0.7" />
        </>
      )}
    </SvgWrapper>
  )
}

export function IconSend(props: IconProps) {
  return (
    <SvgWrapper {...props}>
      {(id) => (
        <>
          <defs>
            <linearGradient id={`${id}-a`} x1="0%" y1="0%" x2="100%" y2="100%">
              <stop offset="0%" stopColor="#38bdf8" />
              <stop offset="100%" stopColor="#6366f1" />
            </linearGradient>
          </defs>
          <path d="M22 2L11 13M22 2l-7 20-4-9-9-4 20-7z" fill={`url(#${id}-a)`} />
        </>
      )}
    </SvgWrapper>
  )
}

export function IconTrash(props: IconProps) {
  return (
    <SvgWrapper {...props}>
      {(id) => (
        <>
          <defs>
            <linearGradient id={`${id}-a`} x1="0%" y1="0%" x2="100%" y2="100%">
              <stop offset="0%" stopColor="#f43f5e" />
              <stop offset="100%" stopColor="#be123c" />
            </linearGradient>
          </defs>
          <path d="M3 6h18M8 6V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2" fill="none" stroke={`url(#${id}-a)`} strokeWidth="2" strokeLinecap="round" />
          <path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6" fill={`url(#${id}-a)`} opacity="0.3" />
          <path d="M10 11v6M14 11v6" stroke={`url(#${id}-a)`} strokeWidth="2" strokeLinecap="round" fill="none" />
        </>
      )}
    </SvgWrapper>
  )
}

export function IconEye(props: IconProps) {
  return (
    <SvgWrapper {...props}>
      {(id) => (
        <>
          <defs>
            <linearGradient id={`${id}-a`} x1="0%" y1="0%" x2="100%" y2="100%">
              <stop offset="0%" stopColor="#38bdf8" />
              <stop offset="100%" stopColor="#a78bfa" />
            </linearGradient>
          </defs>
          <path d="M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z" fill="none" stroke={`url(#${id}-a)`} strokeWidth="2" />
          <circle cx="12" cy="12" r="3" fill={`url(#${id}-a)`} />
        </>
      )}
    </SvgWrapper>
  )
}

// ─── PROTOCOLS & SOURCES ────────────────────────────────────────────────────────

export function IconMllp(props: IconProps) {
  return (
    <SvgWrapper {...props}>
      {(id) => (
        <>
          <defs>
            <linearGradient id={`${id}-a`} x1="0%" y1="0%" x2="100%" y2="100%">
              <stop offset="0%" stopColor="#f472b6" />
              <stop offset="100%" stopColor="#e879f9" />
            </linearGradient>
          </defs>
          <path d="M4 4v16M4 12h6l2-4 2 8 2-4h4" stroke={`url(#${id}-a)`} strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" fill="none" />
          <circle cx="20" cy="12" r="2" fill="#e879f9" />
        </>
      )}
    </SvgWrapper>
  )
}

export function IconHttp(props: IconProps) {
  return (
    <SvgWrapper {...props}>
      {(id) => (
        <>
          <defs>
            <linearGradient id={`${id}-a`} x1="0%" y1="0%" x2="100%" y2="100%">
              <stop offset="0%" stopColor="#60a5fa" />
              <stop offset="100%" stopColor="#06b6d4" />
            </linearGradient>
          </defs>
          <circle cx="12" cy="12" r="9" fill="none" stroke={`url(#${id}-a)`} strokeWidth="2" />
          <ellipse cx="12" cy="12" rx="4" ry="9" fill="none" stroke={`url(#${id}-a)`} strokeWidth="1.5" />
          <path d="M3 12h18" stroke={`url(#${id}-a)`} strokeWidth="1.5" fill="none" />
        </>
      )}
    </SvgWrapper>
  )
}

export function IconSftp(props: IconProps) {
  return (
    <SvgWrapper {...props}>
      {(id) => (
        <>
          <defs>
            <linearGradient id={`${id}-a`} x1="0%" y1="0%" x2="100%" y2="100%">
              <stop offset="0%" stopColor="#fbbf24" />
              <stop offset="100%" stopColor="#34d399" />
            </linearGradient>
          </defs>
          <path d="M13 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V9l-7-7z" fill="none" stroke={`url(#${id}-a)`} strokeWidth="2" />
          <polyline points="13,2 13,9 20,9" fill="none" stroke={`url(#${id}-a)`} strokeWidth="2" />
          <path d="M8 15l2 2 4-4" stroke={`url(#${id}-a)`} strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" fill="none" />
        </>
      )}
    </SvgWrapper>
  )
}

export function IconDatabase(props: IconProps) {
  return (
    <SvgWrapper {...props}>
      {(id) => (
        <>
          <defs>
            <linearGradient id={`${id}-a`} x1="0%" y1="0%" x2="0%" y2="100%">
              <stop offset="0%" stopColor="#6366f1" />
              <stop offset="100%" stopColor="#8b5cf6" />
            </linearGradient>
          </defs>
          <ellipse cx="12" cy="5" rx="8" ry="3" fill={`url(#${id}-a)`} />
          <path d="M4 5v14c0 1.66 3.58 3 8 3s8-1.34 8-3V5" fill="none" stroke={`url(#${id}-a)`} strokeWidth="2" />
          <path d="M4 12c0 1.66 3.58 3 8 3s8-1.34 8-3" fill="none" stroke={`url(#${id}-a)`} strokeWidth="2" />
        </>
      )}
    </SvgWrapper>
  )
}

export function IconSoap(props: IconProps) {
  return (
    <SvgWrapper {...props}>
      {(id) => (
        <>
          <defs>
            <linearGradient id={`${id}-a`} x1="0%" y1="0%" x2="100%" y2="100%">
              <stop offset="0%" stopColor="#f97316" />
              <stop offset="100%" stopColor="#fbbf24" />
            </linearGradient>
          </defs>
          <rect x="3" y="5" width="18" height="14" rx="2" fill="none" stroke={`url(#${id}-a)`} strokeWidth="2" />
          <path d="M7 9h10M7 13h6" stroke={`url(#${id}-a)`} strokeWidth="1.5" strokeLinecap="round" fill="none" />
          <path d="M3 5l9 7 9-7" stroke={`url(#${id}-a)`} strokeWidth="1.5" fill="none" />
        </>
      )}
    </SvgWrapper>
  )
}

export function IconDicom(props: IconProps) {
  return (
    <SvgWrapper {...props}>
      {(id) => (
        <>
          <defs>
            <linearGradient id={`${id}-a`} x1="0%" y1="0%" x2="100%" y2="100%">
              <stop offset="0%" stopColor="#06b6d4" />
              <stop offset="50%" stopColor="#3b82f6" />
              <stop offset="100%" stopColor="#8b5cf6" />
            </linearGradient>
          </defs>
          <rect x="3" y="3" width="18" height="18" rx="3" fill="#0f172a" stroke={`url(#${id}-a)`} strokeWidth="2" />
          <circle cx="12" cy="12" r="5" fill="none" stroke={`url(#${id}-a)`} strokeWidth="2" />
          <path d="M12 7v10M7 12h10" stroke={`url(#${id}-a)`} strokeWidth="1" fill="none" opacity="0.5" />
        </>
      )}
    </SvgWrapper>
  )
}

export function IconJavaScript(props: IconProps) {
  return (
    <SvgWrapper {...props}>
      {(id) => (
        <>
          <defs>
            <linearGradient id={`${id}-a`} x1="0%" y1="0%" x2="100%" y2="100%">
              <stop offset="0%" stopColor="#fbbf24" />
              <stop offset="100%" stopColor="#f59e0b" />
            </linearGradient>
          </defs>
          <rect x="2" y="2" width="20" height="20" rx="3" fill={`url(#${id}-a)`} />
          <text x="7" y="17" fontFamily="monospace" fontSize="11" fontWeight="bold" fill="#1e293b">JS</text>
        </>
      )}
    </SvgWrapper>
  )
}

export function IconEmail(props: IconProps) {
  return (
    <SvgWrapper {...props}>
      {(id) => (
        <>
          <defs>
            <linearGradient id={`${id}-a`} x1="0%" y1="0%" x2="100%" y2="100%">
              <stop offset="0%" stopColor="#f472b6" />
              <stop offset="100%" stopColor="#fb923c" />
            </linearGradient>
          </defs>
          <rect x="2" y="4" width="20" height="16" rx="2" fill="none" stroke={`url(#${id}-a)`} strokeWidth="2" />
          <polyline points="2,4 12,13 22,4" stroke={`url(#${id}-a)`} strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" fill="none" />
        </>
      )}
    </SvgWrapper>
  )
}

export function IconFtp(props: IconProps) {
  return (
    <SvgWrapper {...props}>
      {(id) => (
        <>
          <defs>
            <linearGradient id={`${id}-a`} x1="0%" y1="0%" x2="100%" y2="100%">
              <stop offset="0%" stopColor="#a78bfa" />
              <stop offset="100%" stopColor="#6366f1" />
            </linearGradient>
          </defs>
          <path d="M4 20h16" stroke={`url(#${id}-a)`} strokeWidth="2" strokeLinecap="round" fill="none" />
          <path d="M12 4v12M8 8l4-4 4 4" stroke={`url(#${id}-a)`} strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" fill="none" />
          <circle cx="6" cy="16" r="2" fill={`url(#${id}-a)`} />
          <circle cx="18" cy="16" r="2" fill={`url(#${id}-a)`} />
        </>
      )}
    </SvgWrapper>
  )
}

export function IconTcp(props: IconProps) {
  return (
    <SvgWrapper {...props}>
      {(id) => (
        <>
          <defs>
            <linearGradient id={`${id}-a`} x1="0%" y1="0%" x2="100%" y2="100%">
              <stop offset="0%" stopColor="#22d3ee" />
              <stop offset="100%" stopColor="#0ea5e9" />
            </linearGradient>
          </defs>
          <circle cx="6" cy="6" r="3" fill={`url(#${id}-a)`} />
          <circle cx="18" cy="6" r="3" fill={`url(#${id}-a)`} />
          <circle cx="6" cy="18" r="3" fill={`url(#${id}-a)`} />
          <circle cx="18" cy="18" r="3" fill={`url(#${id}-a)`} />
          <path d="M9 6h6M9 18h6M6 9v6M18 9v6" stroke={`url(#${id}-a)`} strokeWidth="1.5" fill="none" />
        </>
      )}
    </SvgWrapper>
  )
}

// ─── FORMATS ────────────────────────────────────────────────────────────────────

export function IconHl7(props: IconProps) {
  return (
    <SvgWrapper {...props}>
      {(id) => (
        <>
          <defs>
            <linearGradient id={`${id}-a`} x1="0%" y1="0%" x2="100%" y2="100%">
              <stop offset="0%" stopColor="#f97316" />
              <stop offset="100%" stopColor="#ef4444" />
            </linearGradient>
          </defs>
          <rect x="2" y="2" width="20" height="20" rx="3" fill={`url(#${id}-a)`} />
          <text x="4" y="16" fontFamily="monospace" fontSize="9" fontWeight="bold" fill="#fff">HL7</text>
        </>
      )}
    </SvgWrapper>
  )
}

export function IconX12(props: IconProps) {
  return (
    <SvgWrapper {...props}>
      {(id) => (
        <>
          <defs>
            <linearGradient id={`${id}-a`} x1="0%" y1="0%" x2="100%" y2="100%">
              <stop offset="0%" stopColor="#6366f1" />
              <stop offset="100%" stopColor="#a78bfa" />
            </linearGradient>
          </defs>
          <rect x="2" y="2" width="20" height="20" rx="3" fill={`url(#${id}-a)`} />
          <text x="4" y="16" fontFamily="monospace" fontSize="9" fontWeight="bold" fill="#fff">X12</text>
        </>
      )}
    </SvgWrapper>
  )
}

export function IconFhir(props: IconProps) {
  return (
    <SvgWrapper {...props}>
      {(id) => (
        <>
          <defs>
            <linearGradient id={`${id}-a`} x1="0%" y1="0%" x2="100%" y2="100%">
              <stop offset="0%" stopColor="#f43f5e" />
              <stop offset="50%" stopColor="#e879f9" />
              <stop offset="100%" stopColor="#818cf8" />
            </linearGradient>
          </defs>
          <path d="M12 4c-2 0-4 2-4 4s4 5 4 8c0-3 4-6 4-8s-2-4-4-4z" fill={`url(#${id}-a)`} />
          <path d="M12 16v4" stroke={`url(#${id}-a)`} strokeWidth="2" strokeLinecap="round" fill="none" />
          <circle cx="8" cy="18" r="1.5" fill="#f43f5e" />
          <circle cx="16" cy="18" r="1.5" fill="#818cf8" />
        </>
      )}
    </SvgWrapper>
  )
}

export function IconCda(props: IconProps) {
  return (
    <SvgWrapper {...props}>
      {(id) => (
        <>
          <defs>
            <linearGradient id={`${id}-a`} x1="0%" y1="0%" x2="100%" y2="100%">
              <stop offset="0%" stopColor="#10b981" />
              <stop offset="100%" stopColor="#14b8a6" />
            </linearGradient>
          </defs>
          <rect x="2" y="2" width="20" height="20" rx="3" fill={`url(#${id}-a)`} />
          <text x="3" y="16" fontFamily="monospace" fontSize="8" fontWeight="bold" fill="#fff">CDA</text>
        </>
      )}
    </SvgWrapper>
  )
}

export function IconJson(props: IconProps) {
  return (
    <SvgWrapper {...props}>
      {(id) => (
        <>
          <defs>
            <linearGradient id={`${id}-a`} x1="0%" y1="0%" x2="100%" y2="100%">
              <stop offset="0%" stopColor="#fbbf24" />
              <stop offset="100%" stopColor="#f97316" />
            </linearGradient>
          </defs>
          <path d="M8 3c-2 0-3 1-3 3v3c0 1.5-1 2.5-2 3 1 .5 2 1.5 2 3v3c0 2 1 3 3 3" stroke={`url(#${id}-a)`} strokeWidth="2" strokeLinecap="round" fill="none" />
          <path d="M16 3c2 0 3 1 3 3v3c0 1.5 1 2.5 2 3-1 .5-2 1.5-2 3v3c0 2-1 3-3 3" stroke={`url(#${id}-a)`} strokeWidth="2" strokeLinecap="round" fill="none" />
        </>
      )}
    </SvgWrapper>
  )
}

export function IconXml(props: IconProps) {
  return (
    <SvgWrapper {...props}>
      {(id) => (
        <>
          <defs>
            <linearGradient id={`${id}-a`} x1="0%" y1="0%" x2="100%" y2="100%">
              <stop offset="0%" stopColor="#06b6d4" />
              <stop offset="100%" stopColor="#0ea5e9" />
            </linearGradient>
          </defs>
          <path d="M8 8l-4 4 4 4" stroke={`url(#${id}-a)`} strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" fill="none" />
          <path d="M16 8l4 4-4 4" stroke={`url(#${id}-a)`} strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" fill="none" />
          <path d="M14 4l-4 16" stroke={`url(#${id}-a)`} strokeWidth="2" strokeLinecap="round" fill="none" />
        </>
      )}
    </SvgWrapper>
  )
}

export function IconPdf(props: IconProps) {
  return (
    <SvgWrapper {...props}>
      {(id) => (
        <>
          <defs>
            <linearGradient id={`${id}-a`} x1="0%" y1="0%" x2="100%" y2="100%">
              <stop offset="0%" stopColor="#ef4444" />
              <stop offset="100%" stopColor="#dc2626" />
            </linearGradient>
          </defs>
          <rect x="3" y="2" width="18" height="20" rx="2" fill={`url(#${id}-a)`} />
          <text x="5" y="15" fontFamily="monospace" fontSize="7" fontWeight="bold" fill="#fff">PDF</text>
        </>
      )}
    </SvgWrapper>
  )
}

export function IconCsv(props: IconProps) {
  return (
    <SvgWrapper {...props}>
      {(id) => (
        <>
          <defs>
            <linearGradient id={`${id}-a`} x1="0%" y1="0%" x2="100%" y2="100%">
              <stop offset="0%" stopColor="#34d399" />
              <stop offset="100%" stopColor="#10b981" />
            </linearGradient>
          </defs>
          <rect x="3" y="2" width="18" height="20" rx="2" fill={`url(#${id}-a)`} />
          <text x="5" y="15" fontFamily="monospace" fontSize="7" fontWeight="bold" fill="#fff">CSV</text>
        </>
      )}
    </SvgWrapper>
  )
}

// ─── HEALTHCARE & DOMAIN ────────────────────────────────────────────────────────

export function IconPatient(props: IconProps) {
  return (
    <SvgWrapper {...props}>
      {(id) => (
        <>
          <defs>
            <linearGradient id={`${id}-a`} x1="0%" y1="0%" x2="100%" y2="100%">
              <stop offset="0%" stopColor="#38bdf8" />
              <stop offset="100%" stopColor="#06b6d4" />
            </linearGradient>
          </defs>
          <circle cx="12" cy="8" r="4" fill={`url(#${id}-a)`} />
          <path d="M4 21v-2a6 6 0 0 1 6-6h4a6 6 0 0 1 6 6v2" fill={`url(#${id}-a)`} opacity="0.7" />
        </>
      )}
    </SvgWrapper>
  )
}

export function IconHospital(props: IconProps) {
  return (
    <SvgWrapper {...props}>
      {(id) => (
        <>
          <defs>
            <linearGradient id={`${id}-a`} x1="0%" y1="0%" x2="100%" y2="100%">
              <stop offset="0%" stopColor="#f43f5e" />
              <stop offset="100%" stopColor="#60a5fa" />
            </linearGradient>
          </defs>
          <rect x="4" y="4" width="16" height="18" rx="2" fill="none" stroke={`url(#${id}-a)`} strokeWidth="2" />
          <path d="M12 8v6M9 11h6" stroke={`url(#${id}-a)`} strokeWidth="2" strokeLinecap="round" fill="none" />
          <rect x="9" y="18" width="6" height="4" fill={`url(#${id}-a)`} opacity="0.5" />
        </>
      )}
    </SvgWrapper>
  )
}

export function IconLab(props: IconProps) {
  return (
    <SvgWrapper {...props}>
      {(id) => (
        <>
          <defs>
            <linearGradient id={`${id}-a`} x1="0%" y1="0%" x2="100%" y2="100%">
              <stop offset="0%" stopColor="#a78bfa" />
              <stop offset="50%" stopColor="#c084fc" />
              <stop offset="100%" stopColor="#e879f9" />
            </linearGradient>
          </defs>
          <path d="M9 2v7l-5 9a2 2 0 0 0 1.8 3h12.4a2 2 0 0 0 1.8-3l-5-9V2" fill="none" stroke={`url(#${id}-a)`} strokeWidth="2" strokeLinecap="round" />
          <path d="M9 2h6" stroke={`url(#${id}-a)`} strokeWidth="2" strokeLinecap="round" fill="none" />
          <circle cx="10" cy="15" r="1.5" fill="#a78bfa" />
          <circle cx="14" cy="17" r="1" fill="#e879f9" />
        </>
      )}
    </SvgWrapper>
  )
}

export function IconPharmacy(props: IconProps) {
  return (
    <SvgWrapper {...props}>
      {(id) => (
        <>
          <defs>
            <linearGradient id={`${id}-a`} x1="0%" y1="0%" x2="100%" y2="100%">
              <stop offset="0%" stopColor="#34d399" />
              <stop offset="100%" stopColor="#10b981" />
            </linearGradient>
          </defs>
          <rect x="5" y="8" width="14" height="13" rx="2" fill="none" stroke={`url(#${id}-a)`} strokeWidth="2" />
          <path d="M8 8V5a4 4 0 0 1 8 0v3" fill="none" stroke={`url(#${id}-a)`} strokeWidth="2" />
          <path d="M12 12v5M9.5 14.5h5" stroke={`url(#${id}-a)`} strokeWidth="2" strokeLinecap="round" fill="none" />
        </>
      )}
    </SvgWrapper>
  )
}

export function IconImaging(props: IconProps) {
  return (
    <SvgWrapper {...props}>
      {(id) => (
        <>
          <defs>
            <linearGradient id={`${id}-a`} x1="0%" y1="0%" x2="100%" y2="100%">
              <stop offset="0%" stopColor="#06b6d4" />
              <stop offset="100%" stopColor="#6366f1" />
            </linearGradient>
          </defs>
          <rect x="3" y="3" width="18" height="18" rx="3" fill="#0f172a" stroke={`url(#${id}-a)`} strokeWidth="2" />
          <circle cx="12" cy="12" r="4" fill="none" stroke={`url(#${id}-a)`} strokeWidth="1.5" />
          <circle cx="12" cy="12" r="1.5" fill={`url(#${id}-a)`} />
          <path d="M3 3l4 4M21 3l-4 4M3 21l4-4M21 21l-4-4" stroke={`url(#${id}-a)`} strokeWidth="1" fill="none" opacity="0.5" />
        </>
      )}
    </SvgWrapper>
  )
}

export function IconImmunisation(props: IconProps) {
  return (
    <SvgWrapper {...props}>
      {(id) => (
        <>
          <defs>
            <linearGradient id={`${id}-a`} x1="0%" y1="0%" x2="100%" y2="100%">
              <stop offset="0%" stopColor="#22d3ee" />
              <stop offset="100%" stopColor="#10b981" />
            </linearGradient>
          </defs>
          <path d="M19 3l-7 7M15 7l2-2M9 13l-4 4" stroke={`url(#${id}-a)`} strokeWidth="2" strokeLinecap="round" fill="none" />
          <rect x="8" y="10" width="8" height="3" rx="1" fill={`url(#${id}-a)`} transform="rotate(-45 12 12)" />
          <path d="M5 17l-2 2" stroke={`url(#${id}-a)`} strokeWidth="2" strokeLinecap="round" fill="none" />
          <circle cx="4" cy="20" r="1.5" fill={`url(#${id}-a)`} />
        </>
      )}
    </SvgWrapper>
  )
}

export function IconBilling(props: IconProps) {
  return (
    <SvgWrapper {...props}>
      {(id) => (
        <>
          <defs>
            <linearGradient id={`${id}-a`} x1="0%" y1="0%" x2="100%" y2="100%">
              <stop offset="0%" stopColor="#fbbf24" />
              <stop offset="100%" stopColor="#f59e0b" />
            </linearGradient>
          </defs>
          <circle cx="12" cy="12" r="9" fill="none" stroke={`url(#${id}-a)`} strokeWidth="2" />
          <path d="M12 6v2M12 16v2M9 10c0-1.1 1.3-2 3-2s3 .9 3 2-1.3 2-3 2-3 .9-3 2 1.3 2 3 2" stroke={`url(#${id}-a)`} strokeWidth="2" strokeLinecap="round" fill="none" />
        </>
      )}
    </SvgWrapper>
  )
}

export function IconShield(props: IconProps) {
  return (
    <SvgWrapper {...props}>
      {(id) => (
        <>
          <defs>
            <linearGradient id={`${id}-a`} x1="0%" y1="0%" x2="100%" y2="100%">
              <stop offset="0%" stopColor="#6366f1" />
              <stop offset="100%" stopColor="#8b5cf6" />
            </linearGradient>
          </defs>
          <path d="M12 2l8 4v5c0 5.5-3.8 10.7-8 12-4.2-1.3-8-6.5-8-12V6l8-4z" fill={`url(#${id}-a)`} />
          <path d="M9 12l2 2 4-4" stroke="#fff" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" fill="none" />
        </>
      )}
    </SvgWrapper>
  )
}

export function IconLock(props: IconProps) {
  return (
    <SvgWrapper {...props}>
      {(id) => (
        <>
          <defs>
            <linearGradient id={`${id}-a`} x1="0%" y1="0%" x2="100%" y2="100%">
              <stop offset="0%" stopColor="#f59e0b" />
              <stop offset="100%" stopColor="#fbbf24" />
            </linearGradient>
          </defs>
          <rect x="5" y="11" width="14" height="10" rx="2" fill={`url(#${id}-a)`} />
          <path d="M8 11V7a4 4 0 0 1 8 0v4" fill="none" stroke={`url(#${id}-a)`} strokeWidth="2" />
          <circle cx="12" cy="16" r="1.5" fill="#1e293b" />
        </>
      )}
    </SvgWrapper>
  )
}

export function IconKey(props: IconProps) {
  return (
    <SvgWrapper {...props}>
      {(id) => (
        <>
          <defs>
            <linearGradient id={`${id}-a`} x1="0%" y1="0%" x2="100%" y2="100%">
              <stop offset="0%" stopColor="#fbbf24" />
              <stop offset="100%" stopColor="#f97316" />
            </linearGradient>
          </defs>
          <circle cx="8" cy="15" r="5" fill="none" stroke={`url(#${id}-a)`} strokeWidth="2" />
          <path d="M12.5 11.5L21 3M18 3l3 3M16 5l3 3" stroke={`url(#${id}-a)`} strokeWidth="2" strokeLinecap="round" fill="none" />
        </>
      )}
    </SvgWrapper>
  )
}

export function IconHeartbeat(props: IconProps) {
  return (
    <SvgWrapper {...props}>
      {(id) => (
        <>
          <defs>
            <linearGradient id={`${id}-a`} x1="0%" y1="0%" x2="100%" y2="0%">
              <stop offset="0%" stopColor="#f43f5e" />
              <stop offset="100%" stopColor="#ec4899" />
            </linearGradient>
          </defs>
          <path d="M12 21C12 21 3 14 3 8.5a4.5 4.5 0 0 1 9 0 4.5 4.5 0 0 1 9 0C21 14 12 21 12 21z" fill={`url(#${id}-a)`} />
          <polyline points="3,13 8,13 10,10 12,16 14,11 16,13 21,13" stroke="#fff" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" fill="none" />
        </>
      )}
    </SvgWrapper>
  )
}

export function IconStethoscope(props: IconProps) {
  return (
    <SvgWrapper {...props}>
      {(id) => (
        <>
          <defs>
            <linearGradient id={`${id}-a`} x1="0%" y1="0%" x2="100%" y2="100%">
              <stop offset="0%" stopColor="#06b6d4" />
              <stop offset="100%" stopColor="#0ea5e9" />
            </linearGradient>
          </defs>
          <path d="M4 4v4a6 6 0 0 0 12 0V4" stroke={`url(#${id}-a)`} strokeWidth="2" strokeLinecap="round" fill="none" />
          <circle cx="4" cy="4" r="1.5" fill={`url(#${id}-a)`} />
          <circle cx="16" cy="4" r="1.5" fill={`url(#${id}-a)`} />
          <circle cx="18" cy="16" r="2.5" fill="none" stroke={`url(#${id}-a)`} strokeWidth="2" />
          <path d="M18 13.5V8" stroke={`url(#${id}-a)`} strokeWidth="2" fill="none" />
          <path d="M18 18.5V20a3 3 0 0 1-3 3h-2a3 3 0 0 1-3-3v-1" stroke={`url(#${id}-a)`} strokeWidth="2" fill="none" />
        </>
      )}
    </SvgWrapper>
  )
}

// ─── LOOKUP MAPS ────────────────────────────────────────────────────────────────

export const viewIcons: Record<string, React.ComponentType<IconProps>> = {
  'Dashboard': IconDashboard,
  'Channels': IconChannels,
  'Messages': IconMessages,
  'Queue': IconQueue,
  'Alerts': IconAlerts,
  'Metrics': IconMetrics,
  'FHIR lab': IconFhirLab,
  'Documents': IconDocuments,
  'Scripts': IconScripts,
  'Migrate': IconMigrate,
  'Contracts': IconContracts,
  'Tables': IconTables,
  'AI Mapper': IconMapper,
  'Fleet': IconFleet,
  'Flow map': IconFlowMap,
  'Playground': IconPlayground,
  'Shadow': IconShadow,
  'Certificates': IconCertificates,
  'Users': IconUsers,
  'Activity': IconActivity,
  'Settings': IconSettings,
}

export const sourceIcons: Record<string, React.ComponentType<IconProps>> = {
  'mllp': IconMllp,
  'http': IconHttp,
  'sftp': IconSftp,
  'database': IconDatabase,
  'soap': IconSoap,
  'dicom': IconDicom,
  'dicom_query': IconDicom,
  'dicom_move': IconDicom,
  'dicom_worklist': IconDicom,
  'dicomweb': IconDicom,
  'javascript': IconJavaScript,
}

export const formatIcons: Record<string, React.ComponentType<IconProps>> = {
  'hl7': IconHl7,
  'x12': IconX12,
  'hl7v3': IconCda,
  'fhir': IconFhir,
  'cda': IconCda,
}

/** A half-filled circle: the conventional mark for choosing how bright an interface is. */
export function IconTheme(props: IconProps) {
  return (
    <SvgWrapper {...props}>
      {(id) => (
        <>
          <defs>
            <linearGradient id={`${id}-a`} x1="0%" y1="0%" x2="100%" y2="100%">
              <stop offset="0%" stopColor="#94a3b8" />
              <stop offset="100%" stopColor="#64748b" />
            </linearGradient>
          </defs>
          <circle cx="12" cy="12" r="8.5" fill="none" stroke={`url(#${id}-a)`} strokeWidth="1.5" />
          <path d="M12 3.5a8.5 8.5 0 0 0 0 17z" fill={`url(#${id}-a)`} />
        </>
      )}
    </SvgWrapper>
  )
}

/**
 * sectionIcons gives every section heading in the application an icon.
 *
 * Keyed on the heading text, which is what makes it possible to cover fifty-three call sites across thirty-three files without
 * editing any of them, and what makes a drift test possible: SectionIcons.test.ts reads every literal Section title out of the
 * source and fails when one has no entry here. A heading added later with no icon is a test failure rather than the one plain
 * heading somebody notices six months on.
 *
 * The pairing is meant to be informative rather than decorative. A heading about failures gets the error mark, a heading about
 * patients gets the patient. Where nothing in the set fits the meaning, the closest structural match is used rather than inventing a
 * seventy-ninth icon for one heading.
 */
export const sectionIcons: Record<string, (props: IconProps) => JSX.Element> = {
  // Alerting.
  'Alert rules': IconAlerts,

  // Who may sign in.
  'Sign-on': IconUsers,

  // Channel building.
  'Name it': IconEdit,
  'Where messages arrive': IconMllp,
  'Channel file': IconDocuments,
  'The file this creates': IconDocuments,
  'Change the message': IconMapper,

  // Dashboard and metrics.
  Throughput: IconMetrics,
  'Throughput by channel': IconMetrics,
  'Outcomes, 24h': IconActivity,
  Outcomes: IconActivity,
  'Message types, 24h': IconHl7,
  'Destinations, 24h': IconSend,
  Failures: IconError,
  'Time to handle a message': IconPending,
  'Script time': IconScripts,
  'Transformation steps applied': IconMapper,
  Process: IconRunning,

  // Documents and FHIR.
  Input: IconUpload,
  Options: IconSettings,
  'Conversion decisions': IconInfo,
  'What the reader noticed': IconEye,
  'HL7 v2 message': IconHl7,
  'How to convert it': IconRefresh,
  Validation: IconCheck,
  'The repaired document': IconSuccess,

  // Messages and queue.
  'Find a message': IconSearch,
  'Search inside the messages': IconSearch,
  Messages: IconMessages,
  Destinations: IconSend,

  // Scripts.
  'Script workbench': IconScripts,
  'What completes': IconInfo,

  // Migration.
  'Could not be read': IconWarning,
  'What came across': IconMigrate,

  // Everything else.
  'Flow map': IconFlowMap,
  'AI mapping assistant': IconMapper,
  Passkeys: IconKey,
  'Shared mapping tables': IconTables,
  'Add someone': IconUsers,

  // Found by the drift test rather than by reading, which is the point of having it.
  'Not encrypted': IconWarning,
  'Start from something that already works': IconCopy,
  'Which messages to keep': IconFilter,
  'Change the object': IconImaging,
  'What is actually in this feed?': IconEye,
  'Mapping decisions': IconInfo,
  'Start from a message somebody sent you': IconUpload,
  'Delivery time distribution': IconMetrics,
  'Come across from Mirth': IconMigrate,
  'What would this have done?': IconShadow,
  'Machine credentials': IconKey,
  Fleet: IconFleet,
  Scripts: IconScripts,

  // Headings built from a table rather than written inline. The lookup happens on the real string at run time, so these work the
  // same way - the static test simply cannot see them, which is why they are grouped and labelled here.
  Contradictions: IconError,
  'Worth checking': IconWarning,
  Notes: IconInfo,
  'Would be rejected': IconError,
  'Would lose information': IconWarning,
  'Worth knowing': IconInfo,
  'How this server was started': IconSettings,
}
