import { defineConfig } from 'vitest/config';
import react from '@vitejs/plugin-react';
import tailwindcss from '@tailwindcss/vite';
import { fileURLToPath, URL } from 'node:url';
export default defineConfig({ root:fileURLToPath(new URL('.', import.meta.url)), base:'./', plugins:[react(),tailwindcss()], build:{outDir:'dist',emptyOutDir:true}, test:{environment:'jsdom',setupFiles:['./src/test-setup.ts'],include:['src/**/*.test.ts','src/**/*.test.tsx'],restoreMocks:true} });
