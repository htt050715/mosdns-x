import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

const target = process.env.MOSDNS_DEV_TARGET || 'http://127.0.0.1:9099'
export default defineConfig({
  plugins: [vue(), { name: 'normalize-html-line-endings', transformIndexHtml: { order: 'pre', handler: html => html.replace(/\r\n?/g, '\n') } }],
  publicDir: false,
  server: { proxy: { '/api': target, '/plugins': target, '/metrics': target } },
  build: { outDir: '../coremain/www', emptyOutDir: true, sourcemap: false }
})
