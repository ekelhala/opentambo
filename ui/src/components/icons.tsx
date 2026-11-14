import type { ReactNode } from 'react'

/**
 * tambo icon set: drawn paths, 1.5 stroke, square-leaning joins.
 * One system everywhere; no third-party icon fonts.
 */
function Icon({ children, ...props }: { children: ReactNode } & React.SVGProps<SVGSVGElement>) {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth={1.5}
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      {...props}
    >
      {children}
    </svg>
  )
}

/** The tambo mark: a doorway in a square frame, lamp lit from within. */
export function DoorwayMark({ lit, className }: { lit?: boolean; className?: string }) {
  return (
    <svg className={className ? `${className}${lit ? ' lit' : ''}` : `mark${lit ? ' lit' : ''}`} viewBox="0 0 32 32" aria-hidden="true">
      <rect className="door-frame" x="1" y="1" width="30" height="30" />
      <path className="door-glow" d="M10 27 v-9 a6 6 0 0 1 12 0 v9 z" />
    </svg>
  )
}

export const DownloadIcon = (p: React.SVGProps<SVGSVGElement>) => (
  <Icon {...p}>
    <path d="M12 3v12M6 11l6 6 6-6" />
    <path d="M4 21h16" />
  </Icon>
)

export const ModelsIcon = (p: React.SVGProps<SVGSVGElement>) => (
  <Icon {...p}>
    <path d="M12 3 20 7.5v9L12 21 4 16.5v-9L12 3z" />
    <path d="M4 7.5l8 4.5 8-4.5M12 12v9" />
  </Icon>
)

export const ServersIcon = (p: React.SVGProps<SVGSVGElement>) => (
  <Icon {...p}>
    <rect x="3" y="4" width="18" height="7" rx="1" />
    <rect x="3" y="13" width="18" height="7" rx="1" />
    <path d="M7 7.5h.01M7 16.5h.01" />
  </Icon>
)

export const GatewayIcon = (p: React.SVGProps<SVGSVGElement>) => (
  <Icon {...p}>
    <path d="M4 12h5M15 12h5" />
    <path d="M9 12a3 3 0 0 1 3-3v6a3 3 0 0 1-3-3z" />
    <path d="M15 9v6" />
  </Icon>
)

export const RoundsIcon = (p: React.SVGProps<SVGSVGElement>) => (
  <Icon {...p}>
    <rect x="3" y="3" width="8" height="8" rx="1" />
    <rect x="13" y="3" width="8" height="5" rx="1" />
    <rect x="13" y="10" width="8" height="11" rx="1" />
    <rect x="3" y="13" width="8" height="8" rx="1" />
  </Icon>
)

export const MoonIcon = (p: React.SVGProps<SVGSVGElement>) => (
  <Icon {...p}>
    <path d="M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8z" />
  </Icon>
)

export const SunIcon = (p: React.SVGProps<SVGSVGElement>) => (
  <Icon {...p}>
    <circle cx="12" cy="12" r="4.5" />
    <path d="M12 2.5v2.5M12 19v2.5M2.5 12H5M19 12h2.5M4.9 4.9 6.7 6.7M17.3 17.3l1.8 1.8M19.1 4.9l-1.8 1.8M6.7 17.3l-1.8 1.8" />
  </Icon>
)

export const PlayIcon = (p: React.SVGProps<SVGSVGElement>) => (
  <Icon {...p}>
    <path d="M7 4.5v15l13-7.5-13-7.5z" />
  </Icon>
)

export const StopIcon = (p: React.SVGProps<SVGSVGElement>) => (
  <Icon {...p}>
    <rect x="6" y="6" width="12" height="12" rx="0.5" />
  </Icon>
)

export const CloseIcon = (p: React.SVGProps<SVGSVGElement>) => (
  <Icon {...p}>
    <path d="M6 6l12 12M18 6L6 18" />
  </Icon>
)

export const BackIcon = (p: React.SVGProps<SVGSVGElement>) => (
  <Icon {...p}>
    <path d="M19 12H5M11 6l-6 6 6 6" />
  </Icon>
)

export const CopyIcon = (p: React.SVGProps<SVGSVGElement>) => (
  <Icon {...p}>
    <rect x="9" y="9" width="11" height="11" rx="1" />
    <path d="M5 15H4a1 1 0 0 1-1-1V4a1 1 0 0 1 1-1h10a1 1 0 0 1 1 1v1" />
  </Icon>
)

export const PulseIcon = (p: React.SVGProps<SVGSVGElement>) => (
  <Icon {...p}>
    <path d="M3 12h4l2.5-6 4 12 2.5-6H21" />
  </Icon>
)

export const InfoIcon = (p: React.SVGProps<SVGSVGElement>) => (
  <Icon {...p}>
    <circle cx="12" cy="12" r="9" />
    <path d="M12 11v5M12 7.5v.5" />
  </Icon>
)

export const WarnIcon = (p: React.SVGProps<SVGSVGElement>) => (
  <Icon {...p}>
    <path d="M12 3 2.5 20h19L12 3z" />
    <path d="M12 10v4.5M12 17.5v.5" />
  </Icon>
)

export const CheckIcon = (p: React.SVGProps<SVGSVGElement>) => (
  <Icon {...p}>
    <path d="M4 12.5l5 5L20 6.5" />
  </Icon>
)

export const SearchIcon = (p: React.SVGProps<SVGSVGElement>) => (
  <Icon {...p}>
    <circle cx="11" cy="11" r="7" />
    <path d="M20 20l-4.5-4.5" />
  </Icon>
)

export const ChevronDownIcon = (p: React.SVGProps<SVGSVGElement>) => (
  <Icon {...p}>
    <path d="M5 9l7 7 7-7" />
  </Icon>
)

export const ChevronRightIcon = (p: React.SVGProps<SVGSVGElement>) => (
  <Icon {...p}>
    <path d="M9 5l7 7-7 7" />
  </Icon>
)

export const GearIcon = (p: React.SVGProps<SVGSVGElement>) => (
  <Icon {...p}>
    <circle cx="12" cy="12" r="3" />
    <path d="M19 12a7 7 0 0 0-.1-1.2l2-1.6-2-3.4-2.4 1a7 7 0 0 0-2-1.2L14 3h-4l-.4 2.6a7 7 0 0 0-2 1.2l-2.4-1-2 3.4 2 1.6A7 7 0 0 0 5 12c0 .4 0 .8.1 1.2l-2 1.6 2 3.4 2.4-1a7 7 0 0 0 2 1.2L10 21h4l.4-2.6a7 7 0 0 0 2-1.2l2.4 1 2-3.4-2-1.6c.1-.4.1-.8.1-1.2z" />
  </Icon>
)

export const SpinnerIcon = (p: React.SVGProps<SVGSVGElement>) => (
  <Icon strokeWidth={2.2} {...p}>
    <path d="M12 3a9 9 0 1 0 9 9" />
  </Icon>
)
