import { Toaster as Sonner, type ToasterProps } from 'sonner'
import { CheckIcon, CloseIcon, InfoIcon, WarnIcon } from '@/components/icons'

const Toaster = (props: ToasterProps) => (
  <Sonner
    theme="dark"
    position="bottom-center"
    closeButton={false}
    toastOptions={{ className: 'tambo-toast' }}
    icons={{
      success: <CheckIcon className="size-4" style={{ color: 'var(--t-ok)' }} />,
      info: <InfoIcon className="size-4" style={{ color: 'var(--t-info)' }} />,
      warning: <WarnIcon className="size-4" style={{ color: 'var(--t-warn)' }} />,
      error: <CloseIcon className="size-4" style={{ color: 'var(--t-bad-text)' }} />,
    }}
    {...props}
  />
)

export { Toaster }
