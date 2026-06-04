// Syncs the canonical DoD NIC template schema from otter-go into finch.
//
// The schema at packages/otter-go/internal/nicreg/templates.json is the single
// source of truth (embedded by the Go validator). This copies it next to the
// React form as src/nic/templates.gen.json so the dynamic form and the backend
// validator can never disagree. Wired as predev/prebuild in package.json; the
// generated copy is committed so a fresh checkout works without running it.
//
// Edit the canonical file — NOT the generated copy.
import { copyFileSync, existsSync, mkdirSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const src = resolve(here, '../../otter-go/internal/nicreg/templates.json');
const dest = resolve(here, '../src/nic/templates.gen.json');

// In a container build the context is packages/finch only, so the canonical
// file under packages/otter-go is out of reach. The committed templates.gen.json
// is authoritative there — skip rather than fail the build.
if (!existsSync(src)) {
  console.warn(`[sync-nic-schema] canonical source not reachable (${src}); using committed templates.gen.json`);
  process.exit(0);
}

mkdirSync(dirname(dest), { recursive: true });
copyFileSync(src, dest);
console.log(`[sync-nic-schema] ${src} -> ${dest}`);
