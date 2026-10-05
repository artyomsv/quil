import type { Rect } from '../layout';

export type Dir = 'left' | 'right' | 'up' | 'down';
const EPS = 1e-6;

// neighbour is the pane next to `from` in `dir` among the placed panes: the
// nearest one that shares some of that edge, the longest shared edge breaking
// a tie; '' at an edge.
export function neighbour(panes: { id: string; rect: Rect }[], from: string, dir: Dir): string {
  const a = panes.find((p) => p.id === from)?.rect;
  if (!a) return '';
  let best = '';
  let bestOverlap = 0;
  let bestDist = Infinity;
  for (const p of panes) {
    if (p.id === from) continue;
    const b = p.rect;
    let dist: number;
    let overlap: number;
    if (dir === 'left' || dir === 'right') {
      dist = dir === 'right' ? b.x - (a.x + a.w) : a.x - (b.x + b.w);
      overlap = Math.min(a.y + a.h, b.y + b.h) - Math.max(a.y, b.y);
    } else {
      dist = dir === 'down' ? b.y - (a.y + a.h) : a.y - (b.y + b.h);
      overlap = Math.min(a.x + a.w, b.x + b.w) - Math.max(a.x, b.x);
    }
    if (dist < -EPS || overlap <= EPS) continue;
    if (dist < bestDist - EPS || (Math.abs(dist - bestDist) <= EPS && overlap > bestOverlap)) {
      best = p.id;
      bestDist = dist;
      bestOverlap = overlap;
    }
  }
  return best;
}
