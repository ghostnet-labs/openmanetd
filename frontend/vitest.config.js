import { configDefaults, defineConfig } from 'vitest/config';
import react from '@vitejs/plugin-react';

export default defineConfig({
  plugins: [react()],
  test: {
    environment: 'jsdom',
    globals: true,
    setupFiles: './src/test-setup.js',
    // e2e/ holds the Playwright suite (pnpm run e2e); its *.spec.js files
    // are not Vitest tests.
    exclude: [...configDefaults.exclude, 'e2e/**'],
  },
});
