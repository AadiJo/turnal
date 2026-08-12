import { defineConfig } from 'astro/config';
import sitemap from '@astrojs/sitemap';

// Turnal marketing site.
export default defineConfig({
  site: 'https://turnal.johari-dev.com',
  compressHTML: true,
  devToolbar: { enabled: false },
  integrations: [sitemap()],
});
