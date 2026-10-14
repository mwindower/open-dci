import type {ReactNode} from 'react';
import Link from '@docusaurus/Link';
import useBaseUrl from '@docusaurus/useBaseUrl';
import useDocusaurusContext from '@docusaurus/useDocusaurusContext';
import Layout from '@theme/Layout';
import Heading from '@theme/Heading';

import styles from './index.module.css';

// The key properties, as in the README's "Why open-dci" (each backed by tests
// or measurements, see the linked pages).
const properties: {title: string; body: ReactNode; to: string}[] = [
  {
    title: 'One network per tenant, across all your sites',
    body: 'Every partition keeps its own EVPN fabric, VNIs, route targets and ASNs. open-dci joins a tenant’s networks into one routed network, IPv4 and IPv6, and keeps tenants apart.',
    to: '/docs/introduction/how-it-works',
  },
  {
    title: 'No fabric surgery',
    body: 'The gateways join the fabric like any VTEP. Leaves and spines stay as they are, and the network between the sites only needs plain IPv6.',
    to: '/docs/getting-started/requirements',
  },
  {
    title: 'Stock Linux and FRR',
    body: 'No custom data plane, no special hardware: SRv6 in the Linux kernel, BGP in FRR, on any server.',
    to: '/docs/introduction/design-decisions',
  },
  {
    title: 'Fails gracefully, maintains losslessly',
    body: 'Anycast gateway pairs with BFD: losing a gateway or an exit costs well under a second, and open-dci drain takes a gateway out without losing a packet.',
    to: '/docs/operation/health-and-failover',
  },
  {
    title: 'Safe by default',
    body: 'Prefix allowlists per network, route-target filters and prefix limits per peer, and an SRv6 domain closed at its edge.',
    to: '/docs/operation/srv6-domain-edge',
  },
  {
    title: 'Hands-off',
    body: 'One YAML file per gateway, or the networks straight from metal-apiserver. open-dci reconciles continuously and reports its health as Prometheus metrics.',
    to: '/docs/operation/metrics',
  },
];

const useCases = [
  'A tenant’s private networks in several metal-stack partitions or datacenters, routed as one.',
  'Availability zones with independent fabrics and failure domains, connected per tenant.',
  'Fabrics with different VNI, route-target or ASN plans joined without renumbering.',
  'Many isolated tenants over one shared IPv6 core.',
];

function Hero(): ReactNode {
  const {siteConfig} = useDocusaurusContext();
  return (
    <header className={styles.hero}>
      <div className={styles.heroInner}>
        <img
          className={styles.mascot}
          src={useBaseUrl('/img/mascot.svg')}
          alt="open-dci's mascot: a yellow weaver bird weaving a blue and a green thread, one per tenant, through the knots of partitions A, B and C."
        />
        <div>
          <Heading as="h1" className={styles.title}>
            {siteConfig.title}
          </Heading>
          <p className={styles.tagline}>{siteConfig.tagline}.</p>
          <p className={styles.lede}>
            Like the weaver bird, which weaves many threads into one nest, open-dci weaves each
            tenant’s networks from independent fabrics into one: a DCI gateway on stock Linux
            and FRR, at the exit of every EVPN fabric.
          </p>
          <div className={styles.buttons}>
            <Link className="button button--primary button--lg" to="/docs/getting-started/quick-start">
              Quick start
            </Link>
            <Link className="button button--secondary button--lg" to="/docs/introduction/what-is-open-dci">
              Read the docs
            </Link>
          </div>
          <p className={styles.status}>
            <strong>Alpha</strong> · L3 stitching only (EVPN type-5 ↔ VPNv4/v6) · MIT licensed
          </p>
        </div>
      </div>
    </header>
  );
}

export default function Home(): ReactNode {
  const {siteConfig} = useDocusaurusContext();
  return (
    <Layout title="EVPN ↔ SRv6 DCI gateway" description={siteConfig.tagline}>
      <Hero />
      <main>
        <section className={styles.section}>
          <div className="container">
            <div className={styles.grid}>
              {properties.map((p) => (
                <Link key={p.title} className={styles.card} to={p.to}>
                  <Heading as="h3">{p.title}</Heading>
                  <p>{p.body}</p>
                </Link>
              ))}
            </div>
          </div>
        </section>

        <section className={styles.sectionAlt}>
          <div className="container">
            <Heading as="h2">How a packet travels</Heading>
            <p className={styles.sectionLede}>
              A tenant packet leaves partition A’s fabric over VXLAN, the gateway pair encapsulates
              it in SRv6 to partition B’s anycast SID, and partition B’s gateways hand it to their
              fabric with B’s own VNI. Between partitions, only IPv6.
            </p>
            <img
              className={styles.figure}
              src={useBaseUrl('/img/packet-flow.svg')}
              alt="Animated packet flow across three partitions joined by an IPv6-only core: from partition A's fabric over VXLAN to gateway pair A, in SRv6 to partition B's anycast SID, decapsulated by pair B and forwarded with partition B's VNI into its fabric."
            />
            <p>
              <Link to="/docs/introduction/how-it-works">How it works →</Link>
            </p>
          </div>
        </section>

        <section className={styles.section}>
          <div className="container">
            <Heading as="h2">Use cases</Heading>
            <ul className={styles.useCases}>
              {useCases.map((u) => (
                <li key={u}>{u}</li>
              ))}
            </ul>
          </div>
        </section>
      </main>
    </Layout>
  );
}
