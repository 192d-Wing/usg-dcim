import { useEffect } from 'react';
import { useLogout } from '@refinedev/core';

export function LogoutPage() {
  const { mutate: logout } = useLogout();
  useEffect(() => { logout(); }, [logout]);
  return null;
}
