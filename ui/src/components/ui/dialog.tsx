import * as React from 'react'
import { Dialog as DialogPrimitive } from 'radix-ui'
import { CloseIcon } from '@/components/icons'
import { cn } from 'cn'

export const Dialog = DialogPrimitive.Root
export const DialogTrigger = DialogPrimitive.Trigger

export function DialogContent({
  className,
  children,
  ...props
}: React.ComponentProps<typeof DialogPrimitive.Content>) {
  return (
    <DialogPrimitive.Portal>
      <DialogPrimitive.Overlay className="dialog-overlay" />
      <DialogPrimitive.Content className={cn('dialog-content', className)} {...props}>
        {children}
        <DialogPrimitive.Close
          className="icon-btn absolute top-4 right-4"
          aria-label="Close"
        >
          <CloseIcon />
        </DialogPrimitive.Close>
      </DialogPrimitive.Content>
    </DialogPrimitive.Portal>
  )
}

export function DialogHeader({ className, ...props }: React.ComponentProps<'div'>) {
  return <div className={cn('mb-5 grid gap-1.5', className)} {...props} />
}

export const DialogTitle = DialogPrimitive.Title
export const DialogDescription = DialogPrimitive.Description

export function DialogFooter({ className, ...props }: React.ComponentProps<'div'>) {
  return <div className={cn('dialog-foot', className)} {...props} />
}
