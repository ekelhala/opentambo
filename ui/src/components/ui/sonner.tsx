import { Toaster as Sonner, type ToasterProps } from 'sonner'
import { CircleCheckIcon, TriangleAlertIcon, OctagonXIcon, InfoIcon } from 'lucide-react'

const Toaster = (props: ToasterProps) => (
  <Sonner
    theme="system"
    position="bottom-center"
    icons={{
      success: <CircleCheckIcon className="size-4" />,
      info: <InfoIcon className="size-4" />,
      warning: <TriangleAlertIcon className="size-4" />,
      error: <OctagonXIcon className="size-4" />,
    }}
    style={
      {
        '--normal-bg': 'var(--popover)',
        '--normal-text': 'var(--popover-foreground)',
        '--normal-border': 'var(--border)',
      } as React.CSSProperties
    }
    {...props}
  />
)

export { Toaster }
