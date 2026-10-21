import {themes as prismThemes} from 'prism-react-renderer';
import type {Config} from '@docusaurus/types';
import type * as Preset from '@docusaurus/preset-classic';
import type * as OpenApiPlugin from 'docusaurus-plugin-openapi-docs';

const config: Config = {
  title: 'Returns Processing',
  tagline:
    'The reverse flow: authorize returns against shipped order lines, receive and grade the goods, decide RESTOCK, REFURBISH, LIQUIDATE, DISPOSE or RTV from a versioned policy, and hand restockable units back to the ledger. A WMS-tier Supporting context.',
  favicon: 'img/favicon.svg',

  future: {
    v4: true,
    faster: true,
  },

  url: 'https://iqvo.github.io',
  baseUrl: '/returns-processing/',

  organizationName: 'IQVO',
  projectName: 'returns-processing',
  deploymentBranch: 'gh-pages',
  trailingSlash: false,

  onBrokenLinks: 'throw',
  onBrokenAnchors: 'throw',

  i18n: {
    defaultLocale: 'en',
    locales: ['en'],
  },

  markdown: {
    // The ADR bodies are immutable plain Markdown (for example "grade (A|B|C|SCRAP)"
    // in ADR 0004) and must not be parsed as MDX: .md files are CommonMark,
    // the generated API pages are .mdx.
    format: 'detect',
    mermaid: true,
    hooks: {
      onBrokenMarkdownLinks: 'throw',
    },
  },

  presets: [
    [
      'classic',
      {
        docs: {
          sidebarPath: './sidebars.ts',
          editUrl:
            'https://github.com/IQVO/returns-processing/tree/main/docs/docs/',
          docItemComponent: '@theme/ApiItem',
        },
        blog: false,
        theme: {
          customCss: './src/css/custom.css',
        },
      } satisfies Preset.Options,
    ],
  ],

  plugins: [
    // The ADRs stay in docs/adr/*.md (other repos link to those paths); this
    // second docs instance serves them in place under /docs/adr.
    [
      '@docusaurus/plugin-content-docs',
      {
        id: 'adr',
        path: 'adr',
        routeBasePath: 'docs/adr',
        sidebarPath: './sidebarsAdr.ts',
        // Keep the 0001- prefix in URLs/ids so they match the file names.
        numberPrefixParser: false,
        editUrl: 'https://github.com/IQVO/returns-processing/tree/main/docs/adr/',
      },
    ],
    [
      'docusaurus-plugin-openapi-docs',
      {
        id: 'openapi',
        docsPluginId: 'classic',
        config: {
          'returns-processing': {
            // The single source of truth: the same Spectral-linted spec the
            // service ships and CI gates on. Never hand-transcribed here.
            specPath: '../apis/openapi.yaml',
            outputDir: 'docs/api-reference/rest',
            sidebarOptions: {
              groupPathsBy: 'tag',
              categoryLinkSource: 'tag',
            },
            hideSendButton: true,
          } satisfies OpenApiPlugin.Options,
        },
      },
    ],
  ],

  themes: ['docusaurus-theme-openapi-docs', '@docusaurus/theme-mermaid'],

  themeConfig: {
    colorMode: {
      respectPrefersColorScheme: true,
    },
    navbar: {
      title: 'Returns Processing',
      logo: {
        alt: 'Returns Processing',
        src: 'img/logo.svg',
      },
      items: [
        {
          type: 'docSidebar',
          sidebarId: 'docsSidebar',
          position: 'left',
          label: 'Documentation',
        },
        {
          to: '/docs/api-reference',
          label: 'API Reference',
          position: 'left',
        },
        {
          to: '/docs/adr',
          label: 'ADRs',
          position: 'left',
        },
        {
          href: 'https://github.com/IQVO/returns-processing',
          label: 'GitHub',
          position: 'right',
        },
      ],
    },
    footer: {
      style: 'dark',
      links: [
        {
          title: 'Documentation',
          items: [
            {label: 'Introduction', to: '/docs/overview/introduction'},
            {label: 'Quickstart', to: '/docs/overview/quickstart'},
            {label: 'Runbook', to: '/docs/operations/runbook'},
            {label: 'API Reference', to: '/docs/api-reference'},
            {label: 'MCP tools', to: '/docs/mcp/tools'},
            {label: 'Architecture decisions', to: '/docs/adr'},
          ],
        },
        {
          title: 'Neighbouring contexts',
          items: [
            {label: 'inventory-storage', href: 'https://github.com/IQVO/inventory-storage'},
            {label: 'order-management', href: 'https://github.com/IQVO/order-management'},
            {label: 'product-master', href: 'https://github.com/IQVO/product-master'},
            {label: 'fulfillment-execution', href: 'https://github.com/IQVO/fulfillment-execution'},
            {label: 'exception-management', href: 'https://github.com/IQVO/exception-management'},
          ],
        },
        {
          title: 'Source',
          items: [
            {label: 'GitHub repository', href: 'https://github.com/IQVO/returns-processing'},
            {label: 'OpenAPI spec', href: 'https://raw.githubusercontent.com/IQVO/returns-processing/main/apis/openapi.yaml'},
            {label: 'AsyncAPI spec', href: 'https://raw.githubusercontent.com/IQVO/returns-processing/main/apis/asyncapi.yaml'},
          ],
        },
      ],
      copyright: `Returns Processing — a warehouse-systems bounded context. Built ${new Date().getFullYear()}.`,
    },
    prism: {
      theme: prismThemes.github,
      darkTheme: prismThemes.dracula,
      additionalLanguages: ['bash', 'go', 'json', 'yaml', 'sql'],
    },
  } satisfies Preset.ThemeConfig,
};

export default config;
