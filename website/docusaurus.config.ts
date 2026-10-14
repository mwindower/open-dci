import {themes as prismThemes} from 'prism-react-renderer';
import type {Config} from '@docusaurus/types';
import type * as Preset from '@docusaurus/preset-classic';

// This runs in Node.js - don't use client-side code here (browser APIs, JSX...)

const repo = 'https://github.com/mwindower/open-dci';

const config: Config = {
  title: 'open-dci',
  tagline: 'Stitch EVPN tenant VRFs across independent EVPN/VXLAN domains using SRv6 L3VPN',
  favicon: 'img/mascot.svg',

  future: {
    v4: true,
  },

  // GitHub Pages: https://mwindower.github.io/open-dci/
  url: 'https://mwindower.github.io',
  baseUrl: '/open-dci/',
  organizationName: 'mwindower',
  projectName: 'open-dci',
  trailingSlash: false,

  // every link and anchor must resolve; the docs are cross-linked heavily
  onBrokenLinks: 'throw',
  onBrokenAnchors: 'throw',
  markdown: {
    // .md files are CommonMark (code with <, {, etc.), .mdx files MDX
    format: 'detect',
    hooks: {
      onBrokenMarkdownLinks: 'throw',
      onBrokenMarkdownImages: 'throw',
    },
  },

  i18n: {
    defaultLocale: 'en',
    locales: ['en'],
  },

  presets: [
    [
      'classic',
      {
        docs: {
          sidebarPath: './sidebars.ts',
          editUrl: `${repo}/edit/main/website/`,
        },
        blog: false,
        theme: {
          customCss: './src/css/custom.css',
        },
      } satisfies Preset.Options,
    ],
  ],

  themeConfig: {
    colorMode: {
      respectPrefersColorScheme: true,
    },
    navbar: {
      title: 'open-dci',
      logo: {
        alt: "open-dci's mascot, a weaver bird",
        src: 'img/mascot.svg',
      },
      items: [
        {type: 'docSidebar', sidebarId: 'docs', position: 'left', label: 'Docs'},
        {to: '/docs/getting-started/quick-start', label: 'Quick start', position: 'left'},
        {href: `${repo}/releases`, label: 'Releases', position: 'right'},
        {href: repo, label: 'GitHub', position: 'right'},
      ],
    },
    footer: {
      style: 'dark',
      links: [
        {
          title: 'Docs',
          items: [
            {label: 'What is open-dci', to: '/docs/introduction/what-is-open-dci'},
            {label: 'Quick start', to: '/docs/getting-started/quick-start'},
            {label: 'Configuration', to: '/docs/configuration/overview'},
            {label: 'Operation', to: '/docs/operation/commands'},
          ],
        },
        {
          title: 'Project',
          items: [
            {label: 'GitHub', href: repo},
            {label: 'Releases', href: `${repo}/releases`},
            {label: 'Roadmap', to: '/docs/development/roadmap'},
          ],
        },
        {
          title: 'Related',
          items: [
            {label: 'metal-stack', href: 'https://metal-stack.io'},
            {label: 'FRRouting', href: 'https://frrouting.org'},
          ],
        },
      ],
      copyright: `open-dci is released under the MIT license.`,
    },
    prism: {
      theme: prismThemes.github,
      darkTheme: prismThemes.dracula,
      additionalLanguages: ['bash', 'yaml', 'go'],
    },
  } satisfies Preset.ThemeConfig,
};

export default config;
