import axios, { AxiosError } from 'axios';

export const TOKEN_KEY = 'dcim.token';

export const http = axios.create({
  baseURL: '/api/v1',
});

http.interceptors.request.use((cfg) => {
  const token = localStorage.getItem(TOKEN_KEY);
  if (token) cfg.headers.Authorization = `Bearer ${token}`;
  return cfg;
});

http.interceptors.response.use(
  (r) => r,
  (err: AxiosError<any>) => {
    // Normalise our { error: { code, message, details } } envelope into a plain Error.
    const env = err.response?.data?.error;
    if (env) {
      const e = new Error(env.message || 'request failed');
      (e as any).code = env.code;
      (e as any).statusCode = err.response?.status;
      (e as any).details = env.details;
      return Promise.reject(e);
    }
    return Promise.reject(err);
  },
);
