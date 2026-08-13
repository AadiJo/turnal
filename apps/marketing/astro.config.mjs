import { defineConfig } from 'astro/config';
import sitemap from '@astrojs/sitemap';

// Turnal marketing site.
//
// @astrojs/sitemap is pinned to an exact version in package.json: releases
// after 3.2.1 target the Astro 5 build API and fail this project's build.
// Unpin only together with an Astro major upgrade.
export default defineConfig({
  site: 'https://turnal.johari-dev.com',
  compressHTML: true,
  devToolbar: { enabled: false },
  // `site` above supplies the origin for every generated <loc>.
  integrations: [sitemap()],
});
