// Where every card on the investigation map goes, and the path every line takes.
//
// Kept free of React, like graph.ts, because this is the part that is actually
// hard to get right and impossible to see wrong in a unit test of a component:
// a line drawn through the middle of a card still "renders".
//
// The shape: one ROW per run, oldest at the top, and every row read left to
// right the way the investigation went —
//
//   Picked up  →  Found  →  Recommended  →  Opened
//
// the threads earlier runs left it, what it found (answering some of them),
// what that drove, and the threads it left for the next run. A thread is
// opened at the end of one row and picked up at the start of a later one, so
// the line between them is the jump from one run to the next.

import { EDGE_DRAWN_REVERSED } from './graph';
import type { InvestigationMap, MapEdge, MapNode } from './graph';

export const NODE_WIDTH = 268;
export const NODE_HEIGHT = 92;
/** Between two cards stacked in one lane. Lines cross a row only through these
 *  gaps, so they have to stay clear — see `route`. */
export const CARD_GAP_Y = 22;
/** Between lanes. Wide, because every vertical run of a line lives in one. */
export const LANE_GAP = 96;
export const ROW_GAP = 64;
/** A little extra air between one finding's group and the next. */
export const GROUP_GAP = 14;
const ROW_STEP = NODE_HEIGHT + CARD_GAP_Y;
/** A row is never shorter than two cards. The run's name and summary sit in a
 *  frozen column beside it and are clipped to its height, so a run that found
 *  one thing would otherwise show its name and lose the line saying what it
 *  did at any ordinary zoom. */
const MIN_ROW_HEIGHT = 2 * ROW_STEP - CARD_GAP_Y;

export type Lane = 0 | 1 | 2 | 3;
export const LANE_TITLES: [string, string, string, string] = ['Picked up', 'Found', 'Recommended', 'Opened'];

// The run names and the lane names are not on the canvas: they are frozen
// panes over it (see ThreadMap), so the canvas starts with the first lane.
export const laneLeft = (lane: number) => LANE_GAP + lane * (NODE_WIDTH + LANE_GAP);
export const laneRight = (lane: number) => laneLeft(lane) + NODE_WIDTH;
/** The rail down the right of every row, which the lines that go back up the
 *  page or down to a later run travel along. */
const RAIL_X = laneRight(3) + LANE_GAP / 2;
export const FLOW_WIDTH = laneRight(3) + LANE_GAP;


/** The node id of the card that stands in for a thread in the run that took it
 *  up. One per thread, so it never collides with anything in the payload. */
export const workedCardID = (threadId: string) => `worked:${threadId}`;
/** The node id of the card that stands in for a finding in a later run that
 *  saw it again and did something with it. One per finding per run. */
export const seenCardID = (findingId: string, runRef: string) => `seen:${findingId}@${runRef}`;

/** A card on the canvas: a payload node, or the "picked up" card for one. */
export interface FlowCard {
  id: string;
  node: MapNode;
  lane: Lane;
  x: number;
  y: number;
  /** The row it is in — a run's node id, or '' for the undated row. */
  row: string;
  /** The finding group inside that row, for routing. */
  group: string;
  /** Set on a "picked up" card: the thread it stands in for. */
  threadOf?: string;
  /** Set on a "seen again" card: the finding it stands in for. */
  seenOf?: string;
}

export interface FlowRow {
  ref: string;
  ordinal: number;
  top: number;
  height: number;
  /** Every payload node filed under this run, drawn or not. */
  nodes: MapNode[];
  /** The run's loose cards — nothing links to them — behind "+N more". */
  hidden: MapNode[];
  /** Whether the row has loose cards at all, open or folded. */
  hasLoose: boolean;
  /** How many threads this run picked up. */
  pickedUp: number;
  /** How many earlier findings this run saw again and built on. */
  seenAgain: number;
  moreAt?: { x: number; y: number };
}

export interface Point { x: number; y: number }

export type FlowEdgeKind = MapEdge['kind'] | 'picked_up';

export interface FlowEdge {
  id: string;
  source: string;
  target: string;
  kind: FlowEdgeKind;
  label: string;
  /** The line, as an orthogonal polyline from the source card's edge to the
   *  target card's edge. */
  points: Point[];
  labelAt: Point;
}

export interface FlowGeometry {
  rows: FlowRow[];
  cards: Map<string, FlowCard>;
  edges: FlowEdge[];
  width: number;
  height: number;
  /** Where the rows start. */
  top: number;
}

/** The row a payload node belongs to: its run, or the one row for everything
 *  no run can be found for. */
export const rowKeyOf = (n: MapNode) => (n.runOrdinal ? (n.runRef ?? '') : '');

const SEVERITY_RANK: Record<string, number> = { critical: 0, high: 1, medium: 2, low: 3, info: 4 };

/** Whether a thread gets a second card in a later row. Only when the run that
 *  took it up comes after the one it is filed under, and is a run the map can
 *  place — a card in no particular row says nothing. */
function workedRow(n: MapNode): string {
  if (n.kind !== 'task' || !n.workedIn || !n.workedInOrdinal) return '';
  if (n.runOrdinal && n.workedInOrdinal <= n.runOrdinal) return '';
  return n.workedIn;
}

interface Group {
  key: string;
  lanes: [string[], string[], string[], string[]];
  /** Ordering: the groups that carry the thread story come first. */
  rank: number[];
}

/** placeFlow lays the map out. Pure, and keyed on nothing but the map and
 *  which runs have had their loose cards opened. */
export function placeFlow(map: InvestigationMap, expanded: Set<string>): FlowGeometry {
  const byId = new Map(map.nodes.map((n) => [n.id, n]));

  // The second card for each thread a later run took up.
  const worked = new Map<string, string>(); // thread id → row
  map.nodes.forEach((n) => {
    const row = workedRow(n);
    if (row) worked.set(n.id, row);
  });

  // Every line, in the direction it is drawn (oldest first), with the answer
  // to a thread drawn from where it was answered: the card in the run that
  // picked the thread up, not the card in the run that opened it — so a row
  // reads "picked this up, and found that".
  const drawn: Omit<FlowEdge, 'points' | 'labelAt'>[] = map.edges.map((e) => {
    const back = EDGE_DRAWN_REVERSED[e.kind];
    let source = back ? e.target : e.source;
    const target = back ? e.source : e.target;
    if (e.kind === 'answered_by' && worked.has(source)) source = workedCardID(source);
    return { id: e.id, source, target, kind: e.kind, label: e.label };
  });
  // The same for a finding. It is filed under the run that FOUND it, so when a
  // later run sees it again and opens a thread from it, answers a thread with
  // it, or builds a recommendation on it, that line ran from the finding's
  // original card all the way down the page — through every row in between.
  // When the later run recorded seeing it, that is its own work: it gets a card
  // in that run's row, and the work hangs off that. A link with no sighting
  // behind it is drawn as it is, long or not, because shortening it would
  // claim a re-sighting nobody recorded.
  const rowOfId = (id: string): string => {
    if (id.startsWith('worked:')) return worked.get(id.slice('worked:'.length)) ?? '';
    const n = byId.get(id);
    return n ? rowKeyOf(n) : '';
  };
  const seen = new Map<string, { finding: string; row: string }>();
  drawn.forEach((e) => {
    (['source', 'target'] as const).forEach((end) => {
      const id = e[end];
      const n = byId.get(id);
      if (!n || n.kind !== 'finding') return;
      const there = rowOfId(end === 'source' ? e.target : e.source);
      if (!there || there === rowKeyOf(n) || !(n.seenIn ?? []).includes(there)) return;
      const card = seenCardID(id, there);
      seen.set(card, { finding: id, row: there });
      e[end] = card;
    });
  });
  const nodeOf = (id: string): MapNode => {
    if (id.startsWith('worked:')) return byId.get(id.slice('worked:'.length)) as MapNode;
    const s = seen.get(id);
    return (s ? byId.get(s.finding) : byId.get(id)) as MapNode;
  };

  worked.forEach((row, threadId) => {
    const ordinal = byId.get(threadId)?.workedInOrdinal;
    drawn.push({
      id: `${threadId}->${workedCardID(threadId)}:picked_up`,
      source: threadId,
      target: workedCardID(threadId),
      kind: 'picked_up',
      label: `Picked up in run ${ordinal}`,
    });
  });

  const linked = new Set<string>();
  drawn.forEach((e) => { linked.add(e.source); linked.add(e.target); });
  // A finding a later run built on stays on the canvas where it was found,
  // even when every line now leaves from its later card: it is still the
  // beginning of that work.
  seen.forEach(({ finding }) => linked.add(finding));
  const outOf = new Map<string, typeof drawn>();
  drawn.forEach((e) => {
    const list = outOf.get(e.source);
    if (list) list.push(e);
    else outOf.set(e.source, [e]);
  });
  const into = new Map<string, typeof drawn>();
  drawn.forEach((e) => {
    const list = into.get(e.target);
    if (list) list.push(e);
    else into.set(e.target, [e]);
  });

  // Rows, oldest first, undated last.
  const rowsByRef = new Map<string, FlowRow>();
  const rowOf = (ref: string, ordinal: number) => {
    let row = rowsByRef.get(ref);
    if (!row) {
      row = { ref, ordinal, top: 0, height: 0, nodes: [], hidden: [], hasLoose: false, pickedUp: 0, seenAgain: 0 };
      rowsByRef.set(ref, row);
    }
    return row;
  };
  map.nodes.forEach((n) => rowOf(rowKeyOf(n), n.runOrdinal ?? 0).nodes.push(n));
  worked.forEach((row, threadId) => {
    rowOf(row, byId.get(threadId)?.workedInOrdinal ?? 0).pickedUp += 1;
  });
  seen.forEach(({ row }) => { (rowsByRef.get(row) as FlowRow).seenAgain += 1; });
  const rows = [...rowsByRef.values()].sort((a, b) => {
    if (!a.ordinal !== !b.ordinal) return a.ordinal ? -1 : 1;
    return a.ordinal - b.ordinal;
  });

  const cards = new Map<string, FlowCard>();
  const top = 0;
  let y = top;

  rows.forEach((row) => {
    row.top = y;
    const inRow = (id: string) => {
      const card = byId.get(id);
      if (card) return rowKeyOf(card) === row.ref;
      const thread = id.startsWith('worked:') ? id.slice('worked:'.length) : '';
      if (thread) return worked.get(thread) === row.ref;
      return seen.get(id)?.row === row.ref;
    };
    // A thread is never folded away, linked or not: threads are what this
    // view is about, and on a project whose history predates the links every
    // one of them is loose.
    const shown = row.nodes.filter((n) => linked.has(n.id) || n.kind === 'task');
    const loose = row.nodes.filter((n) => !linked.has(n.id) && n.kind !== 'task');
    row.hasLoose = loose.length > 0;
    const open = expanded.has(row.ref);
    row.hidden = open ? [] : loose;

    // Group around findings: what was picked up and answered by a finding,
    // the finding, what it drove, and what it opened, side by side.
    const groups = new Map<string, Group>();
    const groupOf = new Map<string, string>();
    const severity = (id: string) => SEVERITY_RANK[nodeOf(id)?.severity ?? ''] ?? 5;
    [
      ...shown.filter((n) => n.kind === 'finding').map((n) => n.id),
      ...[...seen.entries()].filter(([, s]) => s.row === row.ref).map(([id]) => id),
    ].forEach((id) => {
      groups.set(id, { key: id, lanes: [[], [id], [], []], rank: [2, severity(id)] });
      groupOf.set(id, id);
    });
    const standalone = (key: string, rank: number[]) => {
      let group = groups.get(key);
      if (!group) {
        group = { key, lanes: [[], [], [], []], rank };
        groups.set(key, group);
      }
      return group;
    };
    const anchorFor = (id: string, edges: typeof drawn | undefined, end: 'source' | 'target') =>
      (edges ?? []).map((e) => e[end]).find((other) => groupOf.has(other) && inRow(other));

    // The picked-up cards, on the finding that answered them where it is in
    // this row. A row leads with those — a thread taken up and answered is
    // the payoff the ledger exists for — then the threads it took up and did
    // not answer yet, then the rest of what it found.
    [...worked.entries()].filter(([, r]) => r === row.ref).forEach(([threadId]) => {
      const id = workedCardID(threadId);
      const anchor = anchorFor(id, outOf.get(id), 'target');
      const group = anchor ? (groups.get(groupOf.get(anchor) as string) as Group) : standalone('~pursued', [1, 0]);
      group.lanes[0].push(id);
      if (anchor) group.rank[0] = 0;
    });
    shown.filter((n) => n.kind === 'recommendation').forEach((n) => {
      const anchor = anchorFor(n.id, into.get(n.id), 'source');
      const group = anchor ? (groups.get(groupOf.get(anchor) as string) as Group) : standalone('~recs', [3, 0]);
      group.lanes[2].push(n.id);
    });
    // A thread with no finding in this row to sit beside is one of two kinds.
    // One that a finding in ANOTHER row opened has a line coming in from the
    // left at its own height, so it goes where the recommendation lane is
    // clear: a group of its own at the bottom. One with no line in from the
    // left at all — every thread on a project whose history predates the
    // links — can go in any free space in its lane, so it fills the top of
    // the Opened lane rather than waiting below everything the run found.
    const free: string[] = [];
    shown.filter((n) => n.kind === 'task').forEach((n) => {
      const anchor = anchorFor(n.id, into.get(n.id), 'source');
      if (anchor) {
        (groups.get(groupOf.get(anchor) as string) as Group).lanes[3].push(n.id);
        return;
      }
      const fedFromLeft = (into.get(n.id) ?? []).some((e) => e.kind !== 'supersedes');
      if (fedFromLeft) standalone('~threads', [4, 0]).lanes[3].push(n.id);
      else free.push(n.id);
    });

    const ranked = [...groups.values()].sort((a, b) => {
      for (let i = 0; i < Math.max(a.rank.length, b.rank.length); i += 1) {
        const d = (a.rank[i] ?? 0) - (b.rank[i] ?? 0);
        if (d !== 0) return d;
      }
      const title = (g: Group) => (g.lanes[1][0] ? nodeOf(g.lanes[1][0])?.title : undefined) ?? g.key;
      return title(a).localeCompare(title(b));
    });

    // Findings that share an outcome sit together. Several findings driving
    // one recommendation is common — a recommendation is built on more than
    // one insight — and ranked independently they land a screen apart, each
    // with a line running the length of the row to reach it. So each group
    // is followed by the others it shares a recommendation, a picked-up
    // thread or an opened thread with, best first.
    const cluster = new Map<string, string>();
    const root = (key: string): string => {
      const up = cluster.get(key);
      if (!up || up === key) return key;
      const top = root(up);
      cluster.set(key, top);
      return top;
    };
    const join = (a: string, b: string) => { cluster.set(root(a), root(b)); };
    const inRowFindings = (edges: typeof drawn | undefined, end: 'source' | 'target') =>
      (edges ?? []).map((e) => e[end]).filter((other) => groupOf.has(other) && inRow(other));
    ranked.forEach((group) => {
      const members = [
        ...group.lanes[0].flatMap((id) => inRowFindings(outOf.get(id), 'target')),
        ...group.lanes[2].flatMap((id) => inRowFindings(into.get(id), 'source')),
        ...group.lanes[3].flatMap((id) => inRowFindings(into.get(id), 'source')),
      ].map((finding) => groupOf.get(finding) as string);
      members.forEach((key) => join(key, group.key));
    });
    const ordered: Group[] = [];
    const placed = new Set<string>();
    ranked.forEach((group) => {
      if (placed.has(group.key)) return;
      ranked.filter((g) => root(g.key) === root(group.key)).forEach((g) => {
        placed.add(g.key);
        ordered.push(g);
      });
    });

    let line = 0; // card rows used so far in this run
    let extra = 0; // group gaps used so far
    const freeSlots: number[] = []; // the Opened lane's empty places, top down
    ordered.forEach((group, gi) => {
      if (gi > 0) extra += GROUP_GAP;
      const [picked, found, recs, opened] = group.lanes;
      // The staircase. A finding's line to a thread it opened runs along the
      // thread's own height across the recommendation lane; starting the
      // threads below the last recommendation keeps that height clear, so the
      // line never runs through a card.
      const openedFrom = found.length > 0 && recs.length > 0 ? recs.length : 0;
      const place = (id: string, lane: Lane, k: number) => {
        cards.set(id, {
          id, node: nodeOf(id), lane,
          x: laneLeft(lane),
          y: y + (line + k) * ROW_STEP + extra,
          row: row.ref,
          group: `${row.ref}#${group.key}`,
          ...(id.startsWith('worked:') ? { threadOf: id.slice('worked:'.length) } : {}),
          ...(seen.has(id) ? { seenOf: seen.get(id)?.finding } : {}),
        });
      };
      picked.forEach((id, k) => place(id, 0, k));
      found.forEach((id, k) => place(id, 1, k));
      recs.forEach((id, k) => place(id, 2, k));
      opened.forEach((id, k) => place(id, 3, openedFrom + k));
      const height = Math.max(picked.length, found.length, recs.length, openedFrom + opened.length, 1);
      for (let k = 0; k < height; k += 1) {
        if (k < openedFrom || k >= openedFrom + opened.length) freeSlots.push(y + (line + k) * ROW_STEP + extra);
      }
      line += height;
    });

    // The free threads, into the Opened lane's empty places from the top, and
    // whatever is left over in a column of their own beneath.
    let overflow = false;
    free.forEach((id) => {
      let cardY = freeSlots.shift();
      if (cardY === undefined) {
        if (!overflow && line > 0) extra += GROUP_GAP;
        overflow = true;
        cardY = y + line * ROW_STEP + extra;
        line += 1;
      }
      cards.set(id, {
        id, node: byId.get(id) as MapNode, lane: 3, x: laneLeft(3), y: cardY,
        row: row.ref, group: `${row.ref}#~free`,
      });
    });

    // The loose cards, if they have been asked for, in a block underneath,
    // each in its own kind's lane. A lane is a heading — "Recommended" — so a
    // finding packed into it to save space would be filed under a stage it
    // never was.
    let rowsUsed = line;
    if (open) {
      if (loose.length > 0 && rowsUsed > 0) extra += GROUP_GAP;
      const byLane = new Map<Lane, MapNode[]>();
      loose.forEach((n) => {
        const lane: Lane = n.kind === 'recommendation' ? 2 : 1;
        byLane.set(lane, [...(byLane.get(lane) ?? []), n]);
      });
      byLane.forEach((list, lane) => list.forEach((n, k) => {
        cards.set(n.id, {
          id: n.id, node: n, lane,
          x: laneLeft(lane),
          y: y + (rowsUsed + k) * ROW_STEP + extra,
          row: row.ref,
          group: `${row.ref}#~loose`,
        });
      }));
      rowsUsed += Math.max(0, ...[...byLane.values()].map((list) => list.length));
    }
    if (row.hasLoose) {
      if (rowsUsed > 0) extra += GROUP_GAP;
      row.moreAt = { x: laneLeft(1), y: y + rowsUsed * ROW_STEP + extra };
      rowsUsed += 1;
    }

    row.height = Math.max(Math.max(rowsUsed, 1) * ROW_STEP - CARD_GAP_Y + extra, MIN_ROW_HEIGHT);
    y += row.height + ROW_GAP;
  });

  const edges = routeAll(drawn, cards);
  return { rows, cards, edges, width: FLOW_WIDTH, height: y - ROW_GAP, top };
}

/** The vertical channels lines travel along, and how many have used each, so
 *  that two lines leaving one gap side by side are not drawn on top of each
 *  other. */
class Channels {
  private used = new Map<string, number>();

  offset(key: string): number {
    const n = this.used.get(key) ?? 0;
    this.used.set(key, n + 1);
    // 0, +6, -6, +12, -12, … out to ±36 and back round, so however many lines
    // share a channel none of them leaves it: the gaps are 96 wide and the
    // cards either side start 48 from the middle.
    const slot = n % 13;
    const out = Math.ceil(slot / 2) * 6;
    return slot % 2 === 1 ? out : -out;
  }
}

function routeAll(
  drawn: Omit<FlowEdge, 'points' | 'labelAt'>[], cards: Map<string, FlowCard>,
): FlowEdge[] {
  const channels = new Channels();
  const out: FlowEdge[] = [];
  drawn.forEach((e) => {
    const from = cards.get(e.source);
    const to = cards.get(e.target);
    // A line to a card that is not on the canvas is a line to nowhere.
    if (!from || !to) return;
    out.push({ ...e, ...route(from, to, channels) });
  });
  return out;
}

/** route draws one line as an orthogonal path that only ever runs through
 *  space no card is in.
 *
 * Every card sits on a grid inside its row — lanes across, card rows down —
 * so there are exactly three kinds of empty space, and each line uses only
 * those:
 *
 * - the gaps between lanes, for any vertical run;
 * - the band just above a card's own row, for crossing lanes going back left;
 * - the rail down the right of the map, for going back up the page or down to
 *   a later run.
 *
 * Forward lines (to a lane further right) drop down the gap after their own
 * lane and run in at the target's height. That height is clear across any
 * lane they pass because of the staircase in placeFlow. Everything else goes
 * out to the rail, along it to the band above the target, back across, and
 * down the gap before the target's lane. A thread continuing another thread
 * stays on the rail and comes in from the right, so the Opened lane reads as
 * the threads' own lineage. */
export function route(
  from: FlowCard, to: FlowCard, channels: Channels = new Channels(),
): { points: Point[]; labelAt: Point } {
  const sy = from.y + NODE_HEIGHT / 2;
  const ty = to.y + NODE_HEIGHT / 2;
  const sx = from.x + NODE_WIDTH;

  if (to.lane > from.lane) {
    const tx = to.x;
    if (sy === ty) {
      return { points: [{ x: sx, y: sy }, { x: tx, y: ty }], labelAt: { x: (sx + laneLeft(from.lane + 1)) / 2, y: sy } };
    }
    // Lines that stay within one finding's group share the gap as a fan, which
    // is the shape a reader expects of one finding driving three things. A
    // line leaving its group gets a lane of its own.
    const off = from.group === to.group ? 0 : channels.offset(`gap${from.lane}`);
    const gx = laneRight(from.lane) + LANE_GAP / 2 + off;
    return {
      points: [{ x: sx, y: sy }, { x: gx, y: sy }, { x: gx, y: ty }, { x: tx, y: ty }],
      labelAt: { x: gx, y: (sy + ty) / 2 },
    };
  }

  const rail = RAIL_X + channels.offset('rail');
  if (from.lane === 3 && to.lane === 3) {
    const tx = to.x + NODE_WIDTH;
    return {
      points: [{ x: sx, y: sy }, { x: rail, y: sy }, { x: rail, y: ty }, { x: tx, y: ty }],
      labelAt: { x: rail, y: (sy + ty) / 2 },
    };
  }

  const band = to.y - CARD_GAP_Y / 2;
  const gx = laneLeft(to.lane) - LANE_GAP / 2 + channels.offset(`gap${to.lane - 1}`);
  return {
    points: [
      { x: sx, y: sy }, { x: rail, y: sy }, { x: rail, y: band },
      { x: gx, y: band }, { x: gx, y: ty }, { x: to.x, y: ty },
    ],
    labelAt: { x: (rail + gx) / 2, y: band },
  };
}

/** roundedPath turns an orthogonal polyline into an SVG path with its corners
 *  softened, which is the difference between a wiring diagram and a map. */
export function roundedPath(points: Point[], radius = 8): string {
  if (points.length === 0) return '';
  let d = `M ${points[0].x} ${points[0].y}`;
  for (let i = 1; i < points.length - 1; i += 1) {
    const prev = points[i - 1];
    const cur = points[i];
    const next = points[i + 1];
    const inLen = Math.hypot(cur.x - prev.x, cur.y - prev.y);
    const outLen = Math.hypot(next.x - cur.x, next.y - cur.y);
    const r = Math.min(radius, inLen / 2, outLen / 2);
    if (r <= 0) {
      d += ` L ${cur.x} ${cur.y}`;
      continue;
    }
    const ax = cur.x - ((cur.x - prev.x) / inLen) * r;
    const ay = cur.y - ((cur.y - prev.y) / inLen) * r;
    const bx = cur.x + ((next.x - cur.x) / outLen) * r;
    const by = cur.y + ((next.y - cur.y) / outLen) * r;
    d += ` L ${ax} ${ay} Q ${cur.x} ${cur.y} ${bx} ${by}`;
  }
  const last = points[points.length - 1];
  return `${d} L ${last.x} ${last.y}`;
}
