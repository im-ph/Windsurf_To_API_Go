import { defineStore } from 'pinia';
import { probeAuth } from '@/api/auth';
import { getStoredPassword, setStoredPassword } from '@/api/request';

export const useAuthStore = defineStore('auth', {
  state: () => ({
    required: false,
    authenticated: false,
    password: getStoredPassword(),
    ready: false,
  }),
  actions: {
    async probe(): Promise<void> {
      const probe = await probeAuth();
      this.required = probe.required;
      this.authenticated = !probe.required || !!probe.valid;
      this.ready = true;
    },
    async login(pw: string): Promise<boolean> {
      setStoredPassword(pw);
      this.password = pw;
      await this.probe();
      return this.authenticated;
    },
    logout(): void {
      setStoredPassword('');
      this.password = '';
      this.authenticated = !this.required;
    },
  },
});
