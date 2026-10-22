import { useNavigate } from '@tanstack/react-router'
import { ResetPassword } from './ResetPassword'
import { useAuthStore } from '@/store/authStore'
import { BrandMark } from './icons/BrandMark'

export function ResetPasswordScreen() {
  const navigate = useNavigate()
  const { updatePassword } = useAuthStore()

  const handleSubmit = async (newPassword: string) => {
    await updatePassword(newPassword)
  }

  const handleSuccess = () => {
    // Navigate back to auth screen (login mode)
    navigate({ to: '/auth', search: { redirect: undefined } })
  }

  return (
    <div className="min-h-screen flex flex-col bg-background">
      <div className="drag-region h-12 flex-shrink-0" style={{ WebkitAppRegion: 'drag' } as React.CSSProperties} />
      <div className="flex-1 flex items-center justify-center p-4">
        <div className="max-w-md w-full bg-background border border-border rounded-lg shadow-xl">
          <div className="p-8">
            <div className="flex flex-col items-center gap-4 mb-6">
              <div className="flex items-center gap-3">
                <BrandMark className="h-8 w-8" />
                <h1 className="text-3xl font-bold">Reliant</h1>
              </div>
            </div>
            
            <ResetPassword
              onSubmit={handleSubmit}
              onSuccess={handleSuccess}
            />
          </div>
        </div>
      </div>
    </div>
  )
}