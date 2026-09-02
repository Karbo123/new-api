/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { useNavigate } from '@tanstack/react-router'
import i18n from 'i18next'
import { useCallback } from 'react'

import {
  getSavedLanguage,
  sanitizeAuthRedirect,
} from '@/features/auth/lib/auth-redirect'
import { applyAuthBundle } from '@/lib/api'
import type { AuthBundle } from '@/stores/auth-store'

/**
 * Hook for handling authentication redirects and user data management
 */
export function useAuthRedirect() {
  const navigate = useNavigate()

  /**
   * Handle successful login
   * @param userData - Optional user data from login response
   * @param redirectTo - Redirect path after login
   */
  // 全部 useCallback：调用方（登录页免密 effect 等）把它们放进依赖数组，
  // 不 memo 会每次渲染产生新引用导致 effect 反复重发请求
  const handleLoginSuccess = useCallback(
    async (bundle: AuthBundle, redirectTo?: string) => {
      applyAuthBundle(bundle)
      const savedLang = getSavedLanguage(bundle.user)
      if (savedLang && savedLang !== i18n.language) {
        await i18n.changeLanguage(savedLang)
      }

      const targetPath =
        sanitizeAuthRedirect(redirectTo, window.location.origin) ?? '/dashboard'
      navigate({ href: targetPath, replace: true })
    },
    [navigate]
  )

  /**
   * Redirect to 2FA page
   */
  const redirectTo2FA = useCallback(() => {
    navigate({ to: '/otp', replace: true })
  }, [navigate])

  /**
   * Redirect to login page
   */
  const redirectToLogin = useCallback(() => {
    navigate({ to: '/sign-in', replace: true })
  }, [navigate])

  /**
   * Redirect to register page
   */
  const redirectToRegister = useCallback(() => {
    navigate({ to: '/sign-up', replace: true })
  }, [navigate])

  return {
    handleLoginSuccess,
    redirectTo2FA,
    redirectToLogin,
    redirectToRegister,
  }
}
