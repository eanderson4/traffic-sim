// netload.ts — network GeoJSON loading, including the chunked-serving
// manifest contract (engine/cmd/demosrv/geojson.go): a network whose
// single-file export would exceed 256 MiB is served as a small MANIFEST
// at /net/{id}.geojson — a FeatureCollection with the "frame" member, an
// empty features array, and a "parts" foreign member listing part URLs in
// lane order (/net/{id}.geojson.{schema}.{hash12}.part-NNN — schema+hash
// pin the generation, so a mid-fetch exporter/scenario change 404s stale
// parts and the manifest is refetched) — plus part files that carry the
// lanes in slices
// (V8's ~537M-char string cap makes a bigger single document unparseable
// in the browser). fetchNetwork resolves both shapes to one collection;
// mergeParts is the pure core (node --test reaches it, no fetch/DOM).

import type { Feature, FeatureCollection, LineString } from "geojson";
import type { LocalFrame } from "./proj.ts";

export interface NetworkFile {
  type: string;
  frame?: LocalFrame;
  features: Array<Feature<LineString>>;
}

// NetworkDoc is the wire shape: a full collection OR a chunk manifest
// (parts is a foreign member — absent on small nets).
export interface NetworkDoc extends NetworkFile {
  parts?: unknown;
}

// mergeParts concatenates the parts' features onto the manifest (its own
// — contractually empty — features first, then parts in order), preserves
// the frame and every other member, and drops "parts": the result is the
// plain NetworkFile the rest of main.ts has always consumed.
export function mergeParts(manifest: NetworkDoc, parts: NetworkFile[]): NetworkFile {
  const features: Array<Feature<LineString>> = [...(manifest.features ?? [])];
  // NOT features.push(...pf): spreading a ~350k-element part overflows the
  // call stack ("Maximum call stack size exceeded" on la-lean, 1.4M lanes).
  for (const p of parts) for (const f of p.features ?? []) features.push(f);
  const merged: NetworkDoc = { ...manifest, features };
  delete merged.parts;
  return merged;
}

// fetchNetwork loads the network GeoJSON, transparently reassembling a
// chunk manifest. Parts are fetched SEQUENTIALLY — six parallel 500 MB
// downloads would spike the tab. Small nets take the unchanged
// single-document path; unknown foreign members are ignored either way.
// A part 404 means the scenario changed mid-fetch (hash-pinned part URLs,
// ADR-0018): refetch the manifest ONCE and start over.
export async function fetchNetwork(url: string): Promise<NetworkFile> {
  return fetchNetworkOnce(url).catch((err) => {
    if (err instanceof PartStaleError) return fetchNetworkOnce(url);
    throw err;
  });
}

class PartStaleError extends Error {}

async function fetchNetworkOnce(url: string): Promise<NetworkFile> {
  // no-cache: the manifest is tiny and MUST revalidate — a stale cached
  // manifest would loop the same 404ing part generation (ADR-0018).
  const res = await fetch(url, { cache: "no-cache" });
  if (!res.ok) throw new Error(`fetch ${url}: ${res.status} ${res.statusText}`);
  const doc = (await res.json()) as NetworkDoc;
  if (!Array.isArray(doc.parts)) return doc as NetworkFile;
  const parts: NetworkFile[] = [];
  for (const partUrl of doc.parts as string[]) {
    const r = await fetch(partUrl);
    if (r.status === 404) throw new PartStaleError(`part ${partUrl} is stale (404)`);
    if (!r.ok) throw new Error(`fetch ${partUrl}: ${r.status} ${r.statusText}`);
    parts.push((await r.json()) as NetworkFile);
  }
  return mergeParts(doc, parts);
}
