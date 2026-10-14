import type {SidebarsConfig} from '@docusaurus/plugin-content-docs';

// The docs, in reading order: what it is, getting it running, configuring
// it, running it, then metal-stack, the lab and development.
const category = (label: string, dir: string, items: string[], collapsed = false) => ({
  type: 'category' as const,
  label,
  collapsed,
  link: {type: 'doc' as const, id: `${dir}/${items[0]}`},
  items: items.slice(1).map((id) => `${dir}/${id}`),
});

const sidebars: SidebarsConfig = {
  docs: [
    category('Introduction', 'introduction', [
      'what-is-open-dci',
      'how-it-works',
      'design-decisions',
      'capabilities',
      'alternatives',
    ]),
    category('Getting started', 'getting-started', ['requirements', 'installation', 'quick-start']),
    category('Configuration', 'configuration', [
      'overview',
      'gateway',
      'transport',
      'peers',
      'networks',
      'route-targets',
      'metal-apiserver',
    ]),
    category('Operation', 'operation', [
      'commands',
      'status',
      'metrics',
      'health-and-failover',
      'maintenance',
      'srv6-domain-edge',
      'adding-partitions-and-networks',
      'what-open-dci-changes',
      'failure-measurements',
    ]),
    category('metal-stack', 'metal-stack', ['overview', 'integration-proposal'], true),
    category('Lab', 'lab', ['overview', 'routing-tables'], true),
    category('Development', 'development', ['overview', 'roadmap', 'phase0-findings', 'phase0b-findings'], true),
  ],
};

export default sidebars;
