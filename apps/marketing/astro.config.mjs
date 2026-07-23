import { defineConfig } from 'astro/config';

// Turnal marketing site.
export default defineConfig({
  site: 'https://turnal.johari-dev.com',
  compressHTML: true,
  devToolbar: { enabled: false },
});
