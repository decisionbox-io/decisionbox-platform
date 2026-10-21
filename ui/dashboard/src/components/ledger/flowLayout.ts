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
/** The column on the left that names each row's run. */
export const RUN_COLUMN = 176;
export const ROW_GAP = 64;
/** A little extra air between one finding's group and the next. */
export const GROUP_GAP = 14;
export const CAPTION_HEIGHT = 34;
const CAPTION_GAP = 18;
/** How wide the grid of a run's loose cards gets, once it has been opened. */
const LOOSE_COLUMNS = 3;
const ROW_STEP = NODE_HEIGHT + CARD_GAP_Y;

export type Lane = 0 | 1 | 2 | 3;
export const LANE_TITLES: [string, string, string, string] = ['Picked up', 'Found', 'Recommended', 'Opened'];

export const laneLeft = (lane: number) => RUN_COLUMN + LANE_GAP + lane * (NODE_WIDTH + LANE_GAP);
export const laneRight = (lane: number) => laneLeft(lane) + NODE_WIDTH;
/** The rail down the right of every row, which the lines that go back up the
 *  page or down to a later run travel along. */
const RAIL_X = laneRight(3) + LANE_GAP / 2;
export const FLOW_WIDTH = laneRight(3) + LANE_GAP;


/** The node id of the card that stands in for a thread in the run that took it
 *  up. One per thread, so it never collides with anything in the payload. */
export const workedCardID = (threadId: string) => `worked:${threadId}`;

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
  /** Where the rows start, below the lane captions. */
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
      row = { ref, ordinal, top: 0, height: 0, nodes: [], hidden: [], hasLoose: false, pickedUp: 0 };
      rowsByRef.set(ref, row);
    }
    return row;
  };
  map.nodes.forEach((n) => rowOf(rowKeyOf(n), n.runOrdinal ?? 0).nodes.push(n));
  worked.forEach((row, threadId) => {
    rowOf(row, byId.get(threadId)?.workedInOrdinal ?? 0).pickedUp += 1;
  });
  const rows = [...rowsByRef.values()].sort((a, b) => {
    if (!a.ordinal !== !b.ordinal) return a.ordinal ? -1 : 1;
    return a.ordinal - b.ordinal;
  });

  const cards = new Map<string, FlowCard>();
  const top = CAPTION_HEIGHT + CAPTION_GAP;
  let y = top;

  rows.forEach((row) => {
    row.top = y;
    const inRow = (id: string) => {
      const card = byId.get(id);
      if (card) return rowKeyOf(card) === row.ref;
      const thread = id.startsWith('worked:') ? id.slice('worked:'.length) : '';
      return Boolean(thread) && worked.get(thread) === row.ref;
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
    const severity = (id: string) => SEVERITY_RANK[byId.get(id)?.severity ?? ''] ?? 5;
    shown.filter((n) => n.kind === 'finding').forEach((n) => {
      groups.set(n.id, { key: n.id, lanes: [[], [n.id], [], []], rank: [2, severity(n.id)] });
      groupOf.set(n.id, n.id);
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
    shown.filter((n) => n.kind === 'task').forEach((n) => {
      const anchor = anchorFor(n.id, into.get(n.id), 'source');
      const group = anchor ? (groups.get(groupOf.get(anchor) as string) as Group) : standalone('~threads', [4, 0]);
      group.lanes[3].push(n.id);
    });

    const ordered = [...groups.values()].sort((a, b) => {
      for (let i = 0; i < Math.max(a.rank.length, b.rank.length); i += 1) {
        const d = (a.rank[i] ?? 0) - (b.rank[i] ?? 0);
        if (d !== 0) return d;
      }
      return (byId.get(a.lanes[1][0])?.title ?? a.key).localeCompare(byId.get(b.lanes[1][0])?.title ?? b.key);
    });

    let line = 0; // card rows used so far in this run
    let extra = 0; // group gaps used so far
    ordered.forEach((group, gi) => {
      if (gi > 0) extra += GROUP_GAP;
      const [picked, found, recs, opened] = group.lanes;
      // The staircase. A finding's line to a thread it opened runs along the
      // thread's own height across the recommendation lane; starting the
      // threads below the last recommendation keeps that height clear, so the
      // line never runs through a card.
      const openedFrom = found.length > 0 && recs.length > 0 ? recs.length : 0;
      const place = (id: string, lane: Lane, k: number) => {
        const node = byId.get(id) ?? byId.get(id.slice('worked:'.length)) as MapNode;
        cards.set(id, {
          id, node, lane,
          x: laneLeft(lane),
          y: y + (line + k) * ROW_STEP + extra,
          row: row.ref,
          group: `${row.ref}#${group.key}`,
          ...(id.startsWith('worked:') ? { threadOf: id.slice('worked:'.length) } : {}),
        });
      };
      picked.forEach((id, k) => place(id, 0, k));
      found.forEach((id, k) => place(id, 1, k));
      recs.forEach((id, k) => place(id, 2, k));
      opened.forEach((id, k) => place(id, 3, openedFrom + k));
      line += Math.max(picked.length, found.length, recs.length, openedFrom + opened.length, 1);
    });

    // The loose cards, if they have been asked for, in a block underneath —
    // they have no links to follow, so a grid costs nothing.
    let rowsUsed = line;
    if (open) {
      if (loose.length > 0 && rowsUsed > 0) extra += GROUP_GAP;
      loose.forEach((n, i) => {
        cards.set(n.id, {
          id: n.id, node: n, lane: (1 + (i % LOOSE_COLUMNS)) as Lane,
          x: laneLeft(1 + (i % LOOSE_COLUMNS)),
          y: y + (rowsUsed + Math.floor(i / LOOSE_COLUMNS)) * ROW_STEP + extra,
          row: row.ref,
          group: `${row.ref}#~loose`,
        });
      });
      rowsUsed += Math.ceil(loose.length / LOOSE_COLUMNS);
    }
    if (row.hasLoose) {
      if (rowsUsed > 0) extra += GROUP_GAP;
      row.moreAt = { x: laneLeft(1), y: y + rowsUsed * ROW_STEP + extra };
      rowsUsed += 1;
    }

    row.height = Math.max(rowsUsed, 1) * ROW_STEP - CARD_GAP_Y + extra;
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
    // 0, +6, -6, +12, -12, … and back round before it leaves the gap.
    const step = Math.ceil(n / 2) * 6 * (n % 2 === 1 ? 1 : -1);
    return ((step + 36) % 73) - 36;
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
