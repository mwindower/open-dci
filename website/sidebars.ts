import type {SidebarsConfig} from '@docusaurus/plugin-content-docs';

// The user documentation, in reading order: what it is, getting it running,
// configuring it, running it, metal-stack. Developer documentation (internals,
// measurements, the lab in detail, design history) lives in the repository's
// docs/ directory.
const category = (label: string, dir: string, items: string[], collapsed = false) => ({
  type: 'category' as const,
  label,
  collapsed,
  link: {type: 'doc' as const, id: `${dir}/${items[0]}`},
  items: items.slice(1).map((id) => `${dir}/${id}`),
});

const sidebars: SidebarsConfig = {
  docs: [
    category('Introduction', 'introduction', ['what-is-open-dci', 'how-it-works', 'design-decisions', 'capabilities']),
    category('Getting started', 'getting-started', ['requirements', 'installation', 'quick-start', 'try-the-lab']),
    category('Configuration', 'configuration', [
      'overview',
      'gateway',
      'transport',
      'peers',
      'networks',
      'route-targets',
    ]),
    category('Operation', 'operation', [
      'commands',
      'status',
      'metrics',
      'api',
      'health-and-failover',
      'maintenance',
      'srv6-domain-edge',
      'adding-partitions-and-networks',
    ]),
    category('metal-stack', 'metal-stack', ['overview', 'controller']),
  ],
};

export default sidebars;
