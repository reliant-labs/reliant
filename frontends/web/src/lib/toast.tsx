import { Toaster as SonnerToaster } from 'sonner';

// Export the Toaster component for use in App.tsx
export function Toaster() {
  return (
    <SonnerToaster
      position="top-center"
      expand={false}
      richColors
      theme="dark"
      closeButton
      visibleToasts={3}
      gap={8}
      toastOptions={{
        style: {
          background: 'rgb(30 30 30)',
          border: '1px solid rgb(60 60 60)',
          color: 'rgb(245 245 245)',
          padding: '16px',
          minHeight: '56px',
        },
        className: 'font-mono text-sm',
        classNames: {
          toast: 'group',
          closeButton:
            'group-hover:opacity-100 opacity-70 transition-opacity bg-neutral-700 hover:bg-neutral-600 border-neutral-600',
          error: 'bg-red-950 border-red-800 text-red-100',
          success: 'bg-green-950 border-green-800 text-green-100',
          warning: 'bg-yellow-950 border-yellow-800 text-yellow-100',
          info: 'bg-blue-950 border-blue-800 text-blue-100',
        },
      }}
    />
  );
}
