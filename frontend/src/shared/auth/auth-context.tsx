import { useCallback, useEffect, useState, type ReactNode } from "react";

import {
  ApiError,
  apiRequest,
  decodeAdminDTO,
  decodeLoggedOut,
  decodeLoginResponseDTO,
  refreshAccessToken,
  setAccessToken,
  subscribeSessionInvalidated,
  type AdminDTO,
} from "@/shared/api/client";
import { AuthContext, type AuthStatus } from "@/shared/auth/auth-state";

export function AuthProvider({ children }: { children: ReactNode }) {
  const [admin, setAdmin] = useState<AdminDTO | null>(null);
  const [status, setStatus] = useState<AuthStatus>("restoring");

  const restoreSession = useCallback(async (): Promise<void> => {
    setStatus("restoring");
    const refreshResult = await refreshAccessToken();
    if (refreshResult === "unavailable") {
      setStatus("unavailable");
      return;
    }
    if (refreshResult === "invalid") {
      // 无有效 refresh cookie 时，尝试受信 IP 免密登录。
      try {
        const trusted = await apiRequest("/api/admin/v1/auth/trusted-login", {
          method: "POST",
          body: {},
          authenticated: false,
          retryAuth: false,
        }, decodeLoginResponseDTO);
        setAccessToken(trusted.tokens.accessToken);
        setAdmin(trusted.admin);
        setStatus("authenticated");
        return;
      } catch (error) {
        setAccessToken(null);
        setAdmin(null);
        // 非白名单 IP 返回 401，正常落到登录页；服务异常则提示重试。
        if (error instanceof ApiError && error.status === 401) {
          setStatus("anonymous");
          return;
        }
        setStatus("unavailable");
        return;
      }
    }

    try {
      const value = await apiRequest("/api/admin/v1/me", { retryAuth: false }, decodeAdminDTO);
      setAdmin(value);
      setStatus("authenticated");
    } catch (error) {
      setAccessToken(null);
      setAdmin(null);
      setStatus(error instanceof ApiError && error.status === 401 ? "anonymous" : "unavailable");
    }
  }, []);

  useEffect(() => {
    const unsubscribe = subscribeSessionInvalidated(() => {
      setAdmin(null);
      setStatus("anonymous");
    });

    const restoreTimer = window.setTimeout(() => {
      void restoreSession();
    }, 0);

    return () => {
      window.clearTimeout(restoreTimer);
      unsubscribe();
    };
  }, [restoreSession]);

  async function login(username: string, password: string): Promise<void> {
    const response = await apiRequest("/api/admin/v1/auth/login", {
      method: "POST",
      body: { username, password },
      authenticated: false,
      retryAuth: false,
    }, decodeLoginResponseDTO);
    setAccessToken(response.tokens.accessToken);
    setAdmin(response.admin);
    setStatus("authenticated");
  }

  async function logout(): Promise<void> {
    try {
      await apiRequest("/api/admin/v1/auth/logout", {
        method: "POST",
        body: {},
        authenticated: false,
        retryAuth: false,
      }, decodeLoggedOut);
    } finally {
      setAccessToken(null);
      setAdmin(null);
      setStatus("anonymous");
    }
  }

  async function changePassword(currentPassword: string, newPassword: string): Promise<void> {
    await apiRequest("/api/admin/v1/me/password", {
      method: "PUT",
      body: { currentPassword, newPassword },
    }, () => undefined);
  }

  return (
    <AuthContext.Provider value={{ admin, status, retryRestore: restoreSession, login, logout, changePassword }}>
      {children}
    </AuthContext.Provider>
  );
}
